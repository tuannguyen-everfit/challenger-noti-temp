package httpx

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Everfit-io/go-service-template/internal/features/notification"
	"github.com/Everfit-io/go-service-template/internal/platform/apperr"
	"github.com/Everfit-io/go-service-template/internal/platform/httpx/health"
	"github.com/Everfit-io/go-service-template/internal/platform/localization"
)

// stubPinger is a Pinger that always succeeds — enough for health.New.
type stubPinger struct{}

func (stubPinger) Ping(context.Context) error { return nil }

// noopIdempotencyCache lets us construct NewRouter without a real Valkey.
// It never reports a cache hit, so Idempotency middleware always falls through
// to the wrapped handler. Set is a no-op.
type noopIdempotencyCache struct{}

func (noopIdempotencyCache) Get(context.Context, string) ([]byte, bool, error) {
	return nil, false, nil
}

func (noopIdempotencyCache) Set(context.Context, string, []byte, time.Duration) error {
	return nil
}

type rejectingVerifier struct{}

func (rejectingVerifier) VerifyAccess(string) (string, error) {
	return "", errors.New("invalid token")
}

// newTestRouter constructs the real NewRouter with minimal viable deps. Feature
// handlers get a nil service: tests here only reach the health probes, the
// 404/405 fallbacks and the Bearer gate in front of feature mounts.
func newTestRouter(t *testing.T) http.Handler {
	t.Helper()
	return NewRouter(RouterDeps{
		Health:                 health.New(stubPinger{}, stubPinger{}, health.Meta{}),
		Notification:           notification.NewHandler(nil),
		AccessVerifier:         rejectingVerifier{},
		IdempotencyCache:       noopIdempotencyCache{},
		HTTPTimeout:            5 * time.Second,
		ThrottleMax:            10,
		ThrottleBacklog:        20,
		ThrottleBacklogTimeout: time.Second,
	})
}

func TestNewRouter_Healthcheck_200(t *testing.T) {
	w := httptest.NewRecorder()
	newTestRouter(t).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/healthcheck", nil))
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
}

func TestNewRouter_Liveness_200(t *testing.T) {
	w := httptest.NewRecorder()
	newTestRouter(t).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/liveness", nil))
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
}

func TestNewRouter_404_ReturnsJSONEnvelope(t *testing.T) {
	w := httptest.NewRecorder()
	newTestRouter(t).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/does-not-exist", nil))

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	var body struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("body not JSON: %q (err: %v)", w.Body.String(), err)
	}
	if body.Code != "ROUTE_NOT_FOUND" {
		t.Errorf("code = %q, want ROUTE_NOT_FOUND", body.Code)
	}
}

func TestNewRouter_405_ReturnsJSONEnvelope(t *testing.T) {
	w := httptest.NewRecorder()
	// POST on /healthcheck (only GET is registered) → chi MethodNotAllowed.
	newTestRouter(t).ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/healthcheck", nil))
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", w.Code)
	}
	var body struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("body not JSON: %q (err: %v)", w.Body.String(), err)
	}
	if body.Code != "METHOD_NOT_ALLOWED" {
		t.Errorf("code = %q, want METHOD_NOT_ALLOWED", body.Code)
	}
}

func TestMethodNotAllowed_HandlerDriven_HasAllowHeader(t *testing.T) {
	// Constructor-level sanity: apperr.MethodNotAllowed(..., methods...) carries
	// Allow on the AppError. The chi router test above covers the fallback path
	// (which can't supply Allow); this confirms the explicit path.
	err := apperr.MethodNotAllowed(localization.CodeMethodNotAllowed, "manual", "GET", "PUT")
	if got := err.Headers.Get("Allow"); got != "GET, PUT" {
		t.Errorf("Allow = %q, want GET, PUT", got)
	}
}

func TestNewRouter_Notifications_RequireBearer(t *testing.T) {
	for _, path := range []string{"/api/v1/notifications", "/api/v1/notifications/summary"} {
		w := httptest.NewRecorder()
		newTestRouter(t).ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != http.StatusUnauthorized {
			t.Errorf("GET %s status = %d, want 401", path, w.Code)
		}
	}
}

func TestNewRouter_Devices_RequireBearer(t *testing.T) {
	for _, method := range []string{http.MethodPut, http.MethodDelete} {
		w := httptest.NewRecorder()
		newTestRouter(t).ServeHTTP(w, httptest.NewRequest(method, "/api/v1/devices/d-1", strings.NewReader(`{"platform":"ios","token":"t"}`)))
		if w.Code != http.StatusUnauthorized {
			t.Errorf("%s /api/v1/devices/d-1 status = %d, want 401", method, w.Code)
		}
	}
}

// countingIdempotencyCache records lookups so a test can prove the cache ran (or not).
type countingIdempotencyCache struct{ gets int }

func (c *countingIdempotencyCache) Get(context.Context, string) ([]byte, bool, error) {
	c.gets++
	return nil, false, nil
}

func (c *countingIdempotencyCache) Set(context.Context, string, []byte, time.Duration) error {
	return nil
}

func TestNewRouter_IdempotencyRunsAfterBearer(t *testing.T) {
	cache := &countingIdempotencyCache{}
	router := NewRouter(RouterDeps{
		Health:           health.New(stubPinger{}, stubPinger{}, health.Meta{}),
		Notification:     notification.NewHandler(nil),
		AccessVerifier:   rejectingVerifier{},
		IdempotencyCache: cache,
	})
	req := httptest.NewRequest(http.MethodPut, "/api/v1/devices/d-1", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer bad")
	req.Header.Set("Idempotency-Key", "k1")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
	if cache.gets != 0 {
		t.Errorf("idempotency cache consulted %d times before auth, want 0", cache.gets)
	}
}

const testInternalSecret = "internal-secret-for-router-tests-only"

func newInternalTestRouter(secret string) http.Handler {
	return NewRouter(RouterDeps{
		Health:                     health.New(stubPinger{}, stubPinger{}, health.Meta{}),
		Notification:               notification.NewHandler(nil),
		AccessVerifier:             rejectingVerifier{},
		IdempotencyCache:           noopIdempotencyCache{},
		NotificationInternalSecret: secret,
	})
}

func TestNewRouter_InternalPurge_NotMountedWithoutSecret(t *testing.T) {
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/internal/notifications/users/x", nil)
	req.Header.Set("Internal-Secret", "anything")
	newInternalTestRouter("").ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 when the secret is unset", w.Code)
	}
}

func TestNewRouter_InternalPurge_RequiresSecretNotBearer(t *testing.T) {
	cases := map[string]struct {
		secret string
		want   int
	}{
		"missing secret": {"", http.StatusUnauthorized},
		"wrong secret":   {"wrong", http.StatusUnauthorized},
		// The right secret reaches the handler without a Bearer token: the malformed id is its 400.
		"right secret": {testInternalSecret, http.StatusBadRequest},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodDelete, "/api/v1/internal/notifications/users/not-an-id", nil)
			if tc.secret != "" {
				req.Header.Set("Internal-Secret", tc.secret)
			}
			newInternalTestRouter(testInternalSecret).ServeHTTP(w, req)
			if w.Code != tc.want {
				t.Errorf("status = %d, want %d; body %s", w.Code, tc.want, w.Body.String())
			}
		})
	}
}
