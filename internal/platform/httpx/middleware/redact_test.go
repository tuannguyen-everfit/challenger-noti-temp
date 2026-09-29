package middleware

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

// lim4k is the default-shaped budget used by most cases.
var lim4k = captureLimits{maxValueLen: 4096, maxFields: 200}

// redactToJSON renders the redacted tree so a test can assert on its text.
func redactToJSON(t *testing.T, raw []byte, ct string, lim captureLimits) string {
	t.Helper()
	v := redactBody(raw, ct, lim)
	if v == nil {
		return ""
	}
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal redacted tree: %v", err)
	}
	return string(b)
}

func TestRedactJSON_HidesSensitiveValuesAtEveryDepth(t *testing.T) {
	raw := []byte(`{
		"identifier":"long@everfit.io",
		"password":"Pass1234!",
		"type":"email",
		"nested":{"refresh_token":"eyJhbGci","keep":"visible"},
		"list":[{"access_token":"abc"},{"ok":1}]
	}`)

	got := redactToJSON(t, raw, "application/json", lim4k)

	for _, leak := range []string{"Pass1234!", "long@everfit.io", "eyJhbGci", "abc"} {
		if strings.Contains(got, leak) {
			t.Errorf("leaked %q in: %s", leak, got)
		}
	}
	// Non-sensitive data must survive, otherwise the capture is useless.
	if !strings.Contains(got, `"type":"email"`) {
		t.Errorf("dropped non-sensitive field: %s", got)
	}
	if !strings.Contains(got, `"keep":"visible"`) {
		t.Errorf("dropped nested non-sensitive field: %s", got)
	}
	if !strings.Contains(got, `"ok":1`) {
		t.Errorf("dropped value inside array: %s", got)
	}
}

func TestRedactJSON_KeepsPayloadShape(t *testing.T) {
	got := redactToJSON(t, []byte(`{"password":"x"}`), "application/json", lim4k)

	var out map[string]any
	if err := json.Unmarshal([]byte(got), &out); err != nil {
		t.Fatalf("output is not valid JSON: %v (%s)", err, got)
	}
	// The key stays so a reader can see the request carried a password.
	if out["password"] != redactedValue {
		t.Errorf("password = %v, want %s", out["password"], redactedValue)
	}
}

func TestRedactJSON_NonJSONNeverEchoed(t *testing.T) {
	// A multipart upload body must never reach the log, even truncated —
	// this is the case that keeps binary/file content out.
	raw := []byte("\x89PNG\r\n\x1a\n binary file content here")

	got := redactToJSON(t, raw, "multipart/form-data; boundary=xyz", lim4k)

	if strings.Contains(got, "binary file content") || strings.Contains(got, "PNG") {
		t.Errorf("echoed non-JSON body: %q", got)
	}
	if !strings.Contains(got, "_not_captured") {
		t.Errorf("want a not-captured marker, got %q", got)
	}
}

func TestRedactJSON_MalformedJSONNotEchoed(t *testing.T) {
	got := redactToJSON(t, []byte(`{"password":"secret-value"`), "application/json", lim4k)

	if strings.Contains(got, "secret-value") {
		t.Errorf("leaked content of malformed JSON: %q", got)
	}
	if !strings.Contains(got, "malformed JSON") {
		t.Errorf("want malformed marker, got %q", got)
	}
}

func TestRedactJSON_TruncatesAtMaxBytes(t *testing.T) {
	raw, err := json.Marshal(map[string]string{"note": strings.Repeat("a", 500)})
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}

	got := redactToJSON(t, raw, "application/json", captureLimits{maxValueLen: 64, maxFields: 200})

	if !strings.Contains(got, truncatedMarker) {
		t.Errorf("want truncation marker, got %q", got)
	}
	if strings.Contains(got, strings.Repeat("a", 100)) {
		t.Errorf("long value was not capped: %q", got)
	}
	// Still a valid object — the whole point of bounding by value rather than
	// by encoded bytes.
	var out map[string]any
	if err := json.Unmarshal([]byte(got), &out); err != nil {
		t.Errorf("truncation broke the JSON: %v (%s)", err, got)
	}
}

func TestRedactJSON_EmptyBody(t *testing.T) {
	if got := redactToJSON(t, nil, "application/json", lim4k); got != "" {
		t.Errorf("empty body should produce no field, got %q", got)
	}
}

func TestIsJSONContentType(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"application/json", true},
		{"application/json; charset=utf-8", true},
		{"Application/JSON", true},
		{"application/merge-patch+json", true},
		{"multipart/form-data", false},
		{"text/plain", false},
		{"", false},
	}
	for _, c := range cases {
		if got := isJSONContentType(c.in); got != c.want {
			t.Errorf("isJSONContentType(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestIsSensitiveKey_SubstringMatch(t *testing.T) {
	sensitive := []string{"password", "new_password", "passwordConfirm", "access_token", "API_KEY", "Authorization", "userEmail"}
	for _, k := range sensitive {
		if !isSensitiveKey(k) {
			t.Errorf("isSensitiveKey(%q) = false, want true", k)
		}
	}
	safe := []string{"type", "limit", "cursor", "challenge_id", "status"}
	for _, k := range safe {
		if isSensitiveKey(k) {
			t.Errorf("isSensitiveKey(%q) = true, want false", k)
		}
	}
}

func TestPickAllowed_OnlyListedKeys(t *testing.T) {
	values := map[string][]string{
		"Content-Type":  {"application/json"},
		"Authorization": {"Bearer eyJhbGci"},
		"Cookie":        {"session=abc"},
	}

	got := pickAllowed(values, []string{"content-type", "authorization"}, 512)

	if got["Content-Type"] != "application/json" {
		t.Errorf("Content-Type = %q", got["Content-Type"])
	}
	// Allowlisted but sensitive → still redacted. The allowlist alone is not
	// trusted to keep credentials out.
	if got["Authorization"] != redactedValue {
		t.Errorf("Authorization = %q, want %s", got["Authorization"], redactedValue)
	}
	if _, ok := got["Cookie"]; ok {
		t.Errorf("Cookie was not allowlisted but appeared: %v", got)
	}
}

func TestPickAllowed_EmptyAllowlistCapturesNothing(t *testing.T) {
	values := map[string][]string{"Content-Type": {"application/json"}}
	if got := pickAllowed(values, nil, 512); got != nil {
		t.Errorf("empty allowlist must capture nothing, got %v", got)
	}
}

func TestRedactBody_AlwaysReturnsAMap(t *testing.T) {
	// One stable type per field keeps log-backend index mappings from
	// conflicting, so even the "can't capture" paths return an object.
	cases := []struct {
		name string
		raw  []byte
		ct   string
	}{
		{"json object", []byte(`{"a":1}`), "application/json"},
		{"multipart", []byte("binary"), "multipart/form-data"},
		{"malformed", []byte(`{"a":`), "application/json"},
	}
	for _, c := range cases {
		v := redactBody(c.raw, c.ct, lim4k)
		if _, ok := v.(map[string]any); !ok {
			t.Errorf("%s: got %T, want map[string]any", c.name, v)
		}
	}
}

func TestRedactBody_FieldBudgetStopsWidePayload(t *testing.T) {
	wide := map[string]any{}
	for i := range 100 {
		wide[string(rune('a'+i%26))+string(rune('0'+i/26))] = i
	}
	raw, err := json.Marshal(wide)
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}

	out, ok := redactBody(raw, "application/json", captureLimits{maxValueLen: 512, maxFields: 10}).(map[string]any)
	if !ok {
		t.Fatalf("want a map")
	}
	if len(out) > 11 { // 10 budgeted nodes + the _truncated marker
		t.Errorf("budget not enforced: %d keys", len(out))
	}
	if out[truncatedKey] != true {
		t.Errorf("want %s marker when the budget runs out: %v", truncatedKey, out)
	}
}

func TestRedactBody_NestedObjectStaysQueryable(t *testing.T) {
	raw := []byte(`{"details":{"profile":{"first_name":"Long","email":"x@y.z"}}}`)

	out, ok := redactBody(raw, "application/json", lim4k).(map[string]any)
	if !ok {
		t.Fatalf("want a map")
	}
	details, ok := out["details"].(map[string]any)
	if !ok {
		t.Fatalf("details is %T, want a nested map", out["details"])
	}
	profile, ok := details["profile"].(map[string]any)
	if !ok {
		t.Fatalf("profile is %T, want a nested map", details["profile"])
	}
	if profile["first_name"] != "Long" {
		t.Errorf("first_name = %v", profile["first_name"])
	}
	if profile["email"] != redactedValue {
		t.Errorf("email = %v, want redacted", profile["email"])
	}
}

func TestPickExcept_LogsEverythingNotDenied(t *testing.T) {
	values := map[string][]string{
		"kind":   {"demure"},
		"as":     {"a"},
		"limit":  {"5"},
		"secret": {"nope"},
		"token":  {"leak"},
	}

	got := pickExcept(values, []string{"secret"}, 512)

	// A param nobody registered still shows up — that is the point of the
	// denylist over an allowlist.
	if got["kind"] != "demure" || got["as"] != "a" || got["limit"] != "5" {
		t.Errorf("unregistered params dropped: %v", got)
	}
	// Explicitly denied → gone entirely.
	if _, ok := got["secret"]; ok {
		t.Errorf("denied key still logged: %v", got)
	}
	// Not denied, but the name looks sensitive → value hidden anyway. This is
	// the safety net that makes log-everything acceptable.
	if got["token"] != redactedValue {
		t.Errorf("token = %q, want %s", got["token"], redactedValue)
	}
}

func TestPickExcept_EmptyDenyLogsAll(t *testing.T) {
	got := pickExcept(map[string][]string{"a": {"1"}, "b": {"2"}}, nil, 512)
	if len(got) != 2 {
		t.Errorf("got %v, want both keys", got)
	}
}

func TestPickExcept_TruncatesLongValue(t *testing.T) {
	got := pickExcept(map[string][]string{"note": {strings.Repeat("x", 100)}}, nil, 10)
	if !strings.Contains(got["note"], truncatedMarker) {
		t.Errorf("note = %q, want truncated", got["note"])
	}
}

func TestIsSensitiveKey_DoesNotMatchOrdinaryNames(t *testing.T) {
	// Regression: "pin" as a substring redacted shipping / pinned / spinner,
	// hiding exactly the fields a debugger wants to see.
	for _, k := range []string{"shipping_address", "pinned_at", "spinner", "mapping"} {
		if isSensitiveKey(k) {
			t.Errorf("isSensitiveKey(%q) = true, want false", k)
		}
	}
}

func TestTruncate_DoesNotSplitRune(t *testing.T) {
	// Cutting by byte in the middle of a multi-byte rune yields invalid UTF-8,
	// which json.Marshal then replaces with U+FFFD.
	got := truncate("Nguyễn Hoàng Long", 4)
	if !utf8.ValidString(got) {
		t.Errorf("truncate produced invalid UTF-8: %q", got)
	}
	if !strings.Contains(got, truncatedMarker) {
		t.Errorf("missing truncation marker: %q", got)
	}
}
