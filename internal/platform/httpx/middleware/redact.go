package middleware

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

// redactedValue replaces any sensitive value that would otherwise reach a log
// record. A fixed marker (rather than dropping the key) keeps the shape of the
// payload visible while hiding the content — you can still tell that a request
// carried a password, just not which one.
const redactedValue = "[REDACTED]"

// truncatedMarker / truncatedKey mark where a limit cut the payload, so a
// reader never mistakes a clipped body for a complete one.
const (
	truncatedMarker = "[truncated]"
	truncatedKey    = "_truncated"
)

// sensitiveKeys are JSON object keys whose VALUE never reaches the log,
// wherever they appear in the payload tree. Matching is case-insensitive and
// substring-based: "password" also covers "new_password"/"passwordConfirm",
// "token" covers "access_token"/"refresh_token"/"reset_token".
//
// Substring matching is deliberate over exact matching — it fails toward
// hiding too much, which is the correct direction for a redaction list. Keep
// entries long enough to not collide with ordinary field names: "pin" was
// dropped because it matches shipping / pinned / spinner; add "pin_code" if a
// PIN flow ever lands.
var sensitiveKeys = []string{
	"password",
	"token",
	"secret",
	"authorization",
	"credential",
	"otp",
	"email",
	"phone",
	"identifier", // auth login sends the email/phone under this name
	"signature",
	"api_key",
	"apikey",
}

// isSensitiveKey reports whether a JSON key's value must be redacted.
func isSensitiveKey(key string) bool {
	k := strings.ToLower(key)
	for _, s := range sensitiveKeys {
		if strings.Contains(k, s) {
			return true
		}
	}
	return false
}

// captureLimits bound what a single captured payload contributes to a log
// record. They replace a byte cap on the encoded form, which is unusable once
// the payload is emitted as a nested object: cutting an object at N bytes
// produces invalid JSON, so the bound has to be structural instead.
type captureLimits struct {
	maxValueLen int // longest string value kept; longer ones are truncated
	maxFields   int // total nodes walked before the rest is dropped
}

// redactBody turns a raw payload into a redacted value ready for slog.Any.
//
// The return is ALWAYS a map, never a bare string, so `request_body` has one
// stable type in the log backend. A field that is sometimes an object and
// sometimes a string breaks index mappings in Elasticsearch and forces a
// second column in ClickHouse.
//
// Non-JSON input is never echoed back — it is summarised by size instead.
// That is what keeps multipart uploads and binary payloads out of the log:
// the endpoints most likely to carry a file are also the ones where dumping
// content would be worst.
func redactBody(raw []byte, contentType string, lim captureLimits) any {
	if len(raw) == 0 {
		return nil
	}
	if !isJSONContentType(contentType) {
		return notCaptured(fmt.Sprintf("content-type %q", contentType), len(raw))
	}

	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		// Claims to be JSON but isn't parseable. Logging the raw text would
		// leak whatever it actually is, so report the failure, not the bytes.
		return notCaptured("malformed JSON", len(raw))
	}

	r := &redactor{lim: lim, budget: lim.maxFields}
	return r.walk(v)
}

func notCaptured(reason string, size int) map[string]any {
	return map[string]any{"_not_captured": reason, "_bytes": size}
}

// redactor walks a decoded JSON tree once, spending a shared node budget so a
// deeply nested or very wide payload can't blow up the log record.
type redactor struct {
	lim    captureLimits
	budget int
}

func (r *redactor) walk(v any) any {
	if r.budget <= 0 {
		return truncatedMarker
	}
	r.budget--

	switch tv := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(tv))
		for k, val := range tv {
			if r.budget <= 0 {
				out[truncatedKey] = true
				break
			}
			if isSensitiveKey(k) {
				out[k] = redactedValue
				r.budget--
				continue
			}
			out[k] = r.walk(val)
		}
		return out
	case []any:
		out := make([]any, 0, len(tv))
		for _, val := range tv {
			if r.budget <= 0 {
				out = append(out, truncatedMarker)
				break
			}
			out = append(out, r.walk(val))
		}
		return out
	case string:
		return truncate(tv, r.lim.maxValueLen)
	default:
		return v
	}
}

// truncate caps a single string value, backing off to a rune boundary so a
// multi-byte character is never cut in half (most user text here is Vietnamese).
func truncate(s string, maxLen int) string {
	if maxLen <= 0 || len(s) <= maxLen {
		return s
	}
	for maxLen > 0 && !utf8.RuneStart(s[maxLen]) {
		maxLen--
	}
	return s[:maxLen] + "…" + truncatedMarker
}

// isJSONContentType reports whether the body can be safely parsed and redacted.
func isJSONContentType(ct string) bool {
	base, _, _ := strings.Cut(ct, ";")
	base = strings.ToLower(strings.TrimSpace(base))
	return base == "application/json" || strings.HasSuffix(base, "+json")
}

// pickExcept returns every value EXCEPT those whose key appears in deny.
//
// Used for query params, which are opt-in as a group (one switch) rather than
// per key: a `?kind=` added to an endpoint tomorrow shows up without anyone
// registering it. The safety net is still per-key — a value whose name looks
// sensitive is redacted even when nobody thought to deny it.
func pickExcept(values map[string][]string, deny []string, maxValueLen int) map[string]string {
	if len(values) == 0 {
		return nil
	}
	blocked := make(map[string]struct{}, len(deny))
	for _, d := range deny {
		blocked[strings.ToLower(strings.TrimSpace(d))] = struct{}{}
	}

	out := make(map[string]string, len(values))
	for k, vs := range values {
		if _, ok := blocked[strings.ToLower(k)]; ok {
			continue
		}
		if isSensitiveKey(k) {
			out[k] = redactedValue
			continue
		}
		out[k] = truncate(strings.Join(vs, ","), maxValueLen)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// pickAllowed returns the subset of values whose key appears in allow.
// Used for request headers, where opting in per key is the right default:
// Authorization and Cookie are one typo away from a credential in the log.
func pickAllowed(values map[string][]string, allow []string, maxValueLen int) map[string]string {
	if len(allow) == 0 || len(values) == 0 {
		return nil
	}
	index := make(map[string]struct{}, len(allow))
	for _, a := range allow {
		index[strings.ToLower(strings.TrimSpace(a))] = struct{}{}
	}

	out := make(map[string]string)
	for k, vs := range values {
		if _, ok := index[strings.ToLower(k)]; !ok {
			continue
		}
		// An allowlisted key can still be sensitive (someone lists
		// "Authorization"). Redact rather than trusting the allowlist alone.
		if isSensitiveKey(k) {
			out[k] = redactedValue
			continue
		}
		out[k] = truncate(strings.Join(vs, ","), maxValueLen)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
