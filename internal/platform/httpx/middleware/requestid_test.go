package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRequestID_GeneratesUUIDOnEveryRequest(t *testing.T) {
	// Server-only generation: clients never send Request-Id inbound (see the
	// middleware doc comment). Each request gets a fresh UUIDv4.
	next := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)

	RequestID(next).ServeHTTP(rec, req)

	got := rec.Header().Get(headerRequestID)
	if got == "" {
		t.Fatal("Request-Id was not set on the response")
	}
	// UUIDv4 is 36 chars with 4 dashes.
	if len(got) != 36 || strings.Count(got, "-") != 4 {
		t.Errorf("generated id %q does not look like UUIDv4", got)
	}
}

func TestRequestID_IgnoresInboundHeader(t *testing.T) {
	// Defensive: if an upstream LB / proxy ever starts sending Request-Id, we
	// currently overwrite it. This documents that behavior — if we want to
	// honor inbound IDs later, change requestid.go and update this test.
	next := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set(headerRequestID, "should-be-ignored")

	RequestID(next).ServeHTTP(rec, req)

	if got := rec.Header().Get(headerRequestID); got == "should-be-ignored" {
		t.Error("inbound Request-Id was echoed; server-only generation broken")
	}
}

func TestRequestID_HeaderName_IsBareNoXPrefix(t *testing.T) {
	// RFC 6648: bare names for new headers. Catches accidental reintroduction
	// of the X- prefix (which the legacy `X-Request-Id` had).
	if headerRequestID != "Request-Id" {
		t.Errorf("headerRequestID = %q, want \"Request-Id\" (RFC 6648 bare name)", headerRequestID)
	}
}

func TestRequestID_UUIDsAreUnique(t *testing.T) {
	// Quick sanity check that each invocation mints a fresh UUID.
	next := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {})

	seen := make(map[string]bool, 20)
	for range 20 {
		rec := httptest.NewRecorder()
		RequestID(next).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		id := rec.Header().Get(headerRequestID)
		if seen[id] {
			t.Errorf("duplicate Request-Id %q across 20 requests", id)
		}
		seen[id] = true
	}
}

func TestRequestID_StampsContext(t *testing.T) {
	var fromCtx string
	next := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		fromCtx = RequestIDFromContext(r.Context())
	})

	rec := httptest.NewRecorder()
	RequestID(next).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))

	if fromCtx == "" || fromCtx != rec.Header().Get(headerRequestID) {
		t.Errorf("ctx request id = %q, want the echoed header %q", fromCtx, rec.Header().Get(headerRequestID))
	}
}

func TestRequestIDFromContext_Absent(t *testing.T) {
	if got := RequestIDFromContext(httptest.NewRequest(http.MethodGet, "/x", nil).Context()); got != "" {
		t.Errorf("RequestIDFromContext = %q, want empty", got)
	}
}
