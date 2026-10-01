package middleware

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// HeaderIdempotencyKey matches the IETF httpapi idempotency-key draft —
// no `X-` prefix per RFC 6648.
const HeaderIdempotencyKey = "Idempotency-Key"

// IdempotencyCache stores replayable responses. valkey-backed in production,
// in-memory map for tests.
type IdempotencyCache interface {
	Get(ctx context.Context, key string) (cached []byte, hit bool, err error)
	Set(ctx context.Context, key string, value []byte, ttl time.Duration) error
}

// skipCacheKey carries the per-request opt-out flag set by SkipIdempotencyCache.
type skipCacheKey struct{}

// SkipIdempotencyCache opts the current request's response out of the
// Idempotency-Key replay cache. Call it from a handler that owns its own
// dedupe key (e.g. a finalize endpoint deduping on session_id) — otherwise a
// client-supplied Idempotency-Key would replay the stored response and
// shadow the endpoint's own duplicate handling.
//
// Only affects storing THIS response. A request that already matched a cached
// entry was replayed before the handler ran, so an endpoint adopting this must
// stay out of the cache from its first deploy.
//
// No-op when the request did not pass through the Idempotency middleware.
func SkipIdempotencyCache(ctx context.Context) {
	if skip, ok := ctx.Value(skipCacheKey{}).(*bool); ok {
		*skip = true
	}
}

// cachedResponse is the on-cache representation. status + body are mandatory;
// headers preserves what the original handler wrote (Content-Type, etc).
type cachedResponse struct {
	Status  int                 `json:"s"`
	Headers map[string][]string `json:"h,omitempty"`
	Body    []byte              `json:"b,omitempty"`
}

// Idempotency replays a previously seen response for the same Idempotency-Key
// on POST/PUT/PATCH/DELETE. Safe methods (GET/HEAD) pass through. Mount it
// after BearerAuth: the cache key includes the authenticated user.
//
// Replay window defaults to 24h. Missing or empty header → no-op (pass through).
//
// Race window: two concurrent first requests with the same key will both
// execute and the second's response will overwrite the first in cache. For
// strict at-most-once add a SETNX lock pre-execution — deferred until first
// real load case demonstrates it matters.
func Idempotency(cache IdempotencyCache, ttl time.Duration) func(http.Handler) http.Handler {
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !isMutatingMethod(r.Method) {
				next.ServeHTTP(w, r)
				return
			}
			key := r.Header.Get(HeaderIdempotencyKey)
			if key == "" {
				next.ServeHTTP(w, r)
				return
			}
			if len(key) > 128 || !isPrintableASCII(key) {
				// Invalid keys go through but skip caching — don't 400 on a header.
				next.ServeHTTP(w, r)
				return
			}

			cacheKey := buildIdempotencyCacheKey(r, key)
			if raw, hit, err := cache.Get(r.Context(), cacheKey); err == nil && hit {
				var cr cachedResponse
				if jErr := json.Unmarshal(raw, &cr); jErr == nil {
					replayResponse(w, cr)
					return
				}
				slog.Default().Warn("idempotency: corrupt cache entry, falling through",
					slog.String("key", key), slog.String("error", "json"))
			}

			cw := &idempotencyCapture{ResponseWriter: w, header: http.Header{}}
			skip := false
			next.ServeHTTP(cw, r.WithContext(context.WithValue(r.Context(), skipCacheKey{}, &skip)))

			if skip {
				// Handler owns its own dedupe key — see SkipIdempotencyCache.
				return
			}
			if cw.statusCode >= 500 || cw.statusCode == 0 {
				// Don't cache server errors — caller retries should not get stuck on a 500.
				return
			}
			payload, err := json.Marshal(cachedResponse{
				Status:  cw.statusCode,
				Headers: pickReplayHeaders(cw.header),
				Body:    cw.body.Bytes(),
			})
			if err != nil {
				slog.Default().Warn("idempotency: marshal failed", slog.String("error", err.Error()))
				return
			}
			if err := cache.Set(r.Context(), cacheKey, payload, ttl); err != nil {
				slog.Default().Warn("idempotency: cache set failed", slog.String("error", err.Error()))
			}
		})
	}
}

// buildIdempotencyCacheKey scopes the client key by caller, method and path so a
// reused key never replays another user's (or another endpoint's) response.
func buildIdempotencyCacheKey(r *http.Request, key string) string {
	return "idempotency:" + UserIDFromContext(r.Context()) + ":" + r.Method + " " + r.URL.Path + ":" + key
}

func isMutatingMethod(m string) bool {
	switch m {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
}

func isPrintableASCII(s string) bool {
	for _, b := range []byte(s) {
		if b < 0x20 || b > 0x7e {
			return false
		}
	}
	return true
}

// pickReplayHeaders selects headers worth replaying. Don't replay hop-by-hop
// or per-request-correlation headers — those should be set fresh by the caller.
func pickReplayHeaders(h http.Header) map[string][]string {
	keep := map[string]bool{
		"Content-Type":  true,
		"Cache-Status":  true,
		"Location":      true,
		"Etag":          true,
		"Last-Modified": true,
	}
	out := map[string][]string{}
	for k, v := range h {
		if keep[http.CanonicalHeaderKey(k)] {
			out[k] = v
		}
	}
	return out
}

func replayResponse(w http.ResponseWriter, cr cachedResponse) {
	for k, vs := range cr.Headers {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.Header().Set("Idempotency-Replayed", "true")
	w.WriteHeader(cr.Status)
	if _, err := w.Write(cr.Body); err != nil {
		slog.Default().Warn("idempotency: replay write failed", slog.String("error", err.Error()))
	}
}

// idempotencyCapture wraps ResponseWriter to capture status + body for caching.
// header is the headers map the handler will populate; we copy it after the
// handler returns (since http.ResponseWriter's Header() is mutated in place).
type idempotencyCapture struct {
	http.ResponseWriter
	header      http.Header
	statusCode  int
	body        bytes.Buffer
	wroteHeader bool
}

func (c *idempotencyCapture) WriteHeader(code int) {
	if c.wroteHeader {
		return
	}
	c.wroteHeader = true
	c.statusCode = code
	// snapshot headers at write-header time (promoted Header() reaches the embedded ResponseWriter).
	for k, v := range c.Header() {
		c.header[strings.ToLower(k)] = append([]string{}, v...)
	}
	c.ResponseWriter.WriteHeader(code)
}

func (c *idempotencyCapture) Write(b []byte) (int, error) {
	if !c.wroteHeader {
		c.WriteHeader(http.StatusOK)
	}
	c.body.Write(b)
	return c.ResponseWriter.Write(b)
}
