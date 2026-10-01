package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type memCache struct {
	mu   sync.Mutex
	data map[string][]byte
}

func newMemCache() *memCache { return &memCache{data: map[string][]byte{}} }

func (c *memCache) Get(_ context.Context, key string) ([]byte, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.data[key]
	return v, ok, nil
}

func (c *memCache) Set(_ context.Context, key string, val []byte, _ time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.data[key] = val
	return nil
}

func TestIdempotency_PassesThroughForGET(t *testing.T) {
	calls := 0
	h := Idempotency(newMemCache(), time.Hour)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(200)
	}))
	for range 2 {
		req := httptest.NewRequest(http.MethodGet, "/x", nil)
		req.Header.Set(HeaderIdempotencyKey, "k1")
		h.ServeHTTP(httptest.NewRecorder(), req)
	}
	if calls != 2 {
		t.Errorf("GET should pass through, calls = %d, want 2", calls)
	}
}

func TestIdempotency_PassesThroughWhenHeaderMissing(t *testing.T) {
	calls := 0
	h := Idempotency(newMemCache(), time.Hour)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(201)
	}))
	for range 2 {
		req := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader("{}"))
		h.ServeHTTP(httptest.NewRecorder(), req)
	}
	if calls != 2 {
		t.Errorf("POST without key should pass through every time, calls = %d", calls)
	}
}

func TestIdempotency_ReplaysCachedResponseForSecondPOST(t *testing.T) {
	calls := 0
	cache := newMemCache()
	h := Idempotency(cache, time.Hour)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(201)
		_, _ = w.Write([]byte(`{"id":"abc"}`))
	}))

	req1 := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader("{}"))
	req1.Header.Set(HeaderIdempotencyKey, "k1")
	rec1 := httptest.NewRecorder()
	h.ServeHTTP(rec1, req1)

	req2 := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader("{}"))
	req2.Header.Set(HeaderIdempotencyKey, "k1")
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req2)

	if calls != 1 {
		t.Errorf("handler calls = %d, want 1 (second is replayed)", calls)
	}
	if rec2.Code != 201 {
		t.Errorf("replay status = %d, want 201", rec2.Code)
	}
	if got := rec2.Body.String(); got != `{"id":"abc"}` {
		t.Errorf("replay body = %q", got)
	}
	if rec2.Header().Get("Idempotency-Replayed") != "true" {
		t.Errorf("replay should set Idempotency-Replayed header")
	}
}

func TestIdempotency_DifferentKeysExecuteSeparately(t *testing.T) {
	calls := 0
	cache := newMemCache()
	h := Idempotency(cache, time.Hour)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(200)
	}))

	for _, k := range []string{"k1", "k2", "k3"} {
		req := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader("{}"))
		req.Header.Set(HeaderIdempotencyKey, k)
		h.ServeHTTP(httptest.NewRecorder(), req)
	}
	if calls != 3 {
		t.Errorf("calls = %d, want 3 (one per unique key)", calls)
	}
}

func TestIdempotency_500NotCached(t *testing.T) {
	calls := 0
	cache := newMemCache()
	h := Idempotency(cache, time.Hour)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(500)
	}))

	for range 2 {
		req := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader("{}"))
		req.Header.Set(HeaderIdempotencyKey, "k1")
		h.ServeHTTP(httptest.NewRecorder(), req)
	}
	if calls != 2 {
		t.Errorf("calls = %d, want 2 (5xx must not be cached)", calls)
	}
}

func TestIdempotency_InvalidKeyPassesThrough(t *testing.T) {
	calls := 0
	cache := newMemCache()
	h := Idempotency(cache, time.Hour)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(200)
	}))

	req := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader("{}"))
	req.Header.Set(HeaderIdempotencyKey, strings.Repeat("x", 200)) // > 128 chars
	h.ServeHTTP(httptest.NewRecorder(), req)

	if calls != 1 {
		t.Errorf("invalid key should still execute, calls = %d", calls)
	}
	// Ensure nothing was cached.
	if _, hit, _ := cache.Get(context.Background(), "idempotency::POST /x:"+strings.Repeat("x", 200)); hit {
		t.Error("invalid key should not be cached")
	}
}

func TestIdempotency_SkipCacheReachesHandlerEveryTime(t *testing.T) {
	calls := 0
	cache := newMemCache()
	h := Idempotency(cache, time.Hour)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		SkipIdempotencyCache(r.Context())
		calls++
		// The endpoint owns its own dedupe key, so the second call answers
		// differently — a replay of the first response would hide that.
		if calls == 1 {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusConflict)
	}))

	last := 0
	for range 2 {
		req := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader("{}"))
		req.Header.Set(HeaderIdempotencyKey, "k-skip")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		last = rec.Code
	}

	if calls != 2 {
		t.Errorf("handler calls = %d, want 2 (opted-out response must not replay)", calls)
	}
	if last != http.StatusConflict {
		t.Errorf("second status = %d, want 409 (handler's own answer, not a replay)", last)
	}
	cache.mu.Lock()
	n := len(cache.data)
	cache.mu.Unlock()
	if n != 0 {
		t.Errorf("cache entries = %d, want 0", n)
	}

	// Outside the middleware there is no flag to set — must be a no-op, not a panic.
	SkipIdempotencyCache(context.Background())
}

func TestIdempotency_KeyScopedByUserMethodAndPath(t *testing.T) {
	calls := 0
	cache := newMemCache()
	h := Idempotency(cache, time.Hour)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusOK)
	}))

	send := func(user, method, path string) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(WithUserID(context.Background(), user), method, path, strings.NewReader("{}"))
		req.Header.Set(HeaderIdempotencyKey, "shared-key")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	send("user-a", http.MethodPost, "/x")
	if rec := send("user-b", http.MethodPost, "/x"); rec.Header().Get("Idempotency-Replayed") != "" {
		t.Error("user-b replayed user-a's response")
	}
	send("user-a", http.MethodPut, "/x")
	send("user-a", http.MethodPost, "/y")
	if calls != 4 {
		t.Errorf("calls = %d, want 4 (user, method and path each scope the key)", calls)
	}
	if rec := send("user-a", http.MethodPost, "/x"); rec.Header().Get("Idempotency-Replayed") != "true" {
		t.Error("same user + method + path + key should replay")
	}
	if _, hit, _ := cache.Get(context.Background(), "idempotency:user-a:POST /x:shared-key"); !hit {
		t.Error("cache key shape changed")
	}
}
