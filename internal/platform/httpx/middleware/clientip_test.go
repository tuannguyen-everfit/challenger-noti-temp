package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClientIP_Forwarded_RFC7239(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Forwarded", `for=192.0.2.43, for="[2001:db8::1]:47011"`)
	r.RemoteAddr = "10.0.0.1:5000"
	if got := extractClientIP(r); got != "192.0.2.43" {
		t.Errorf("got %q, want 192.0.2.43", got)
	}
}

func TestClientIP_Forwarded_QuotedIPv6(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Forwarded", `for="[2001:db8::1]:47011"`)
	if got := extractClientIP(r); got != "2001:db8::1" {
		t.Errorf("got %q, want 2001:db8::1", got)
	}
}

func TestClientIP_XForwardedFor_Leftmost(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Forwarded-For", "203.0.113.7, 10.0.0.1, 10.0.0.2")
	r.RemoteAddr = "10.0.0.2:5000"
	if got := extractClientIP(r); got != "203.0.113.7" {
		t.Errorf("got %q, want 203.0.113.7 (leftmost)", got)
	}
}

func TestClientIP_XRealIP_WhenNoForwarded(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Real-IP", "198.51.100.5")
	r.RemoteAddr = "10.0.0.1:5000"
	if got := extractClientIP(r); got != "198.51.100.5" {
		t.Errorf("got %q, want 198.51.100.5", got)
	}
}

func TestClientIP_FallsBackToRemoteAddr(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "172.16.0.5:5000"
	if got := extractClientIP(r); got != "172.16.0.5" {
		t.Errorf("got %q, want 172.16.0.5 (RemoteAddr without port)", got)
	}
}

func TestClientIP_ForwardedTakesPriorityOverXForwardedFor(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Forwarded", "for=192.0.2.1")
	r.Header.Set("X-Forwarded-For", "203.0.113.7")
	if got := extractClientIP(r); got != "192.0.2.1" {
		t.Errorf("got %q, want 192.0.2.1 (Forwarded > X-Forwarded-For)", got)
	}
}

func TestClientIP_InvalidIPInHeader_FallsThrough(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Forwarded-For", "not-an-ip, 203.0.113.7")
	r.RemoteAddr = "10.0.0.1:5000"
	// Leftmost is junk; we don't silently accept "10.0.0.1" either — we fall
	// back to RemoteAddr so logs don't claim a bogus value.
	if got := extractClientIP(r); got != "10.0.0.1" {
		t.Errorf("got %q, want 10.0.0.1 (RemoteAddr fallback when leftmost is invalid)", got)
	}
}

func TestClientIP_EmptyHeaders_UsesRemoteAddr(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "203.0.113.99:443"
	if got := extractClientIP(r); got != "203.0.113.99" {
		t.Errorf("got %q, want 203.0.113.99", got)
	}
}

func TestClientIP_Middleware_StampsContextLogger(t *testing.T) {
	// Sanity: the middleware doesn't panic and passes through. The slog
	// attribute itself is exercised in logging tests; here we just confirm
	// the chain works end-to-end.
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Forwarded-For", "203.0.113.7")
	w := httptest.NewRecorder()
	ClientIP(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		// noop — just need to confirm the chain runs.
	})).ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
}
