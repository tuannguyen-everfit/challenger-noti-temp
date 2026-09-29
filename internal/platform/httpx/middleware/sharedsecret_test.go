package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSharedSecret_ValidSecret_PassesThrough(t *testing.T) {
	called := false
	h := SharedSecret("top-secret")(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodPost, "/x", nil)
	req.Header.Set("Internal-Secret", "top-secret")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if !called {
		t.Error("next handler not called on valid secret")
	}
	if rec.Code != http.StatusNoContent {
		t.Errorf("status = %d, want 204", rec.Code)
	}
}

func TestSharedSecret_WrongSecret_401(t *testing.T) {
	called := false
	h := SharedSecret("top-secret")(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	req := httptest.NewRequest(http.MethodPost, "/x", nil)
	req.Header.Set("Internal-Secret", "nope")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if called {
		t.Error("next handler called despite wrong secret")
	}
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

func TestSharedSecret_MissingHeader_401(t *testing.T) {
	h := SharedSecret("top-secret")(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("next handler called without secret header")
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/x", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

func TestSharedSecret_EmptyConfigured_FailsClosed(t *testing.T) {
	// An empty configured secret must reject everything — even a request that
	// sends an empty Internal-Secret — so a misconfigured deploy can't expose
	// the internal route unauthenticated.
	h := SharedSecret("")(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("next handler called with empty configured secret")
	}))
	req := httptest.NewRequest(http.MethodPost, "/x", nil)
	req.Header.Set("Internal-Secret", "")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 (fail closed)", rec.Code)
	}
}
