package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestTimeout_PassesThroughWhenZero(t *testing.T) {
	called := false
	h := Timeout(0)(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		called = true
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	if !called {
		t.Error("zero-timeout middleware should be a passthrough")
	}
}

func TestTimeout_EnforcesDeadline(t *testing.T) {
	var sawDeadline error
	h := Timeout(20 * time.Millisecond)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(200 * time.Millisecond):
			sawDeadline = errors.New("handler ran to completion — timeout did not fire")
		case <-r.Context().Done():
			sawDeadline = r.Context().Err()
		}
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	if !errors.Is(sawDeadline, context.DeadlineExceeded) {
		t.Errorf("expected DeadlineExceeded, got %v", sawDeadline)
	}
}

func TestTimeout_PropagatesCtxToHandler(t *testing.T) {
	var deadline time.Time
	var ok bool
	h := Timeout(5 * time.Second)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		deadline, ok = r.Context().Deadline()
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	if !ok {
		t.Fatal("ctx should have a deadline set by the middleware")
	}
	if time.Until(deadline) < 4*time.Second {
		t.Errorf("deadline too close: %v", time.Until(deadline))
	}
}
