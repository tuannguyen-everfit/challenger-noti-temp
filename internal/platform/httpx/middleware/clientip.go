package middleware

import (
	"log/slog"
	"net"
	"net/http"
	"strings"

	"github.com/Everfit-io/go-service-template/internal/platform/logging"
)

// ClientIP extracts the originating client's IP address from forwarding
// headers and stamps it on the request-scoped logger as `client_ip`.
//
// Resolution order (first valid wins):
//  1. RFC 7239 `Forwarded` header — `for=...` parameter
//  2. de-facto `X-Forwarded-For` — leftmost entry (the original client; rightmost
//     entries are intermediate proxies)
//  3. de-facto `X-Real-IP` — single-IP header set by some proxies
//  4. r.RemoteAddr fallback (the immediate peer; behind a load balancer this is
//     the LB, not the user — useful only when no proxy headers are present)
//
// Trust model: this middleware does NOT validate which proxy set the header.
// If the service is exposed directly to the internet (no LB), an attacker can
// forge any X-Forwarded-For value. Deployment guidance: only expose this
// service via a trusted LB / ingress that strips inbound forwarding headers
// from clients and writes its own. The LB → app hop is trusted; the
// client → LB hop is not.
//
// What this middleware does NOT do today:
//   - Validate against a configured trusted-proxy list. Add when needed.
//   - Rate-limit by IP. That's a separate middleware.
//   - Echo the IP back to the client. Stays internal (logs only).
func ClientIP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := extractClientIP(r)
		log := logging.FromContext(r.Context()).With(slog.String("client_ip", ip))
		ctx := logging.ContextWithLogger(r.Context(), log)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// extractClientIP applies the resolution order from the doc comment above.
// Returns the empty string when no source produces a parseable IP — callers
// should treat empty as "unknown" rather than substituting a sentinel.
func extractClientIP(r *http.Request) string {
	if ip := parseForwardedFor(r.Header.Get("Forwarded")); ip != "" {
		return ip
	}
	if ip := parseXForwardedFor(r.Header.Get("X-Forwarded-For")); ip != "" {
		return ip
	}
	if ip := r.Header.Get("X-Real-IP"); validIP(ip) {
		return ip
	}
	// r.RemoteAddr is "host:port" — strip the port.
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr // best effort; unusual transports may produce no port
	}
	return host
}

// parseForwardedFor extracts the for= parameter from an RFC 7239 Forwarded
// header. The header can contain multiple proxy entries separated by commas;
// the leftmost entry's for= is the original client. Values may be quoted and
// may include port and IPv6-bracket notation; we strip both.
//
// Example: `Forwarded: for=192.0.2.43, for="[2001:db8::1]:47011"` → 192.0.2.43
func parseForwardedFor(header string) string {
	if header == "" {
		return ""
	}
	leftEntry, _, _ := strings.Cut(header, ",")
	for _, part := range strings.Split(leftEntry, ";") {
		part = strings.TrimSpace(part)
		k, v, ok := strings.Cut(part, "=")
		if !ok || !strings.EqualFold(strings.TrimSpace(k), "for") {
			continue
		}
		v = strings.Trim(strings.TrimSpace(v), `"`)
		// Try host:port first — handles `[ipv6]:port` and `ipv4:port` cleanly.
		if host, _, err := net.SplitHostPort(v); err == nil {
			v = host
		}
		// Strip IPv6 brackets if SplitHostPort didn't (no-port case: `[ipv6]`).
		v = strings.TrimSuffix(strings.TrimPrefix(v, "["), "]")
		if validIP(v) {
			return v
		}
	}
	return ""
}

// parseXForwardedFor returns the leftmost IP from a comma-separated list.
// Per the de-facto convention, the leftmost entry is the original client.
func parseXForwardedFor(header string) string {
	if header == "" {
		return ""
	}
	left, _, _ := strings.Cut(header, ",")
	left = strings.TrimSpace(left)
	if validIP(left) {
		return left
	}
	return ""
}

func validIP(s string) bool { return net.ParseIP(s) != nil }
