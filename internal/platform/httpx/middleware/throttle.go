package middleware

import (
	"net/http"
	"time"

	"github.com/Everfit-io/go-service-template/internal/platform/apperr"
	"github.com/Everfit-io/go-service-template/internal/platform/httpx/respond"
	"github.com/Everfit-io/go-service-template/internal/platform/localization"
)

// Throttle caps concurrent in-flight requests through the wrapped chain.
//
//	max     — concurrent slots; 0 disables the middleware (pass-through)
//	backlog — extra requests allowed to wait for a slot; rejected immediately when full
//	timeout — max wait time in backlog before returning 503 + Retry-After
//
// Surface on overflow: apperr.ServiceUnavailable(SERVICE_BUSY) → respond.MapError
// emits the standard error envelope plus Retry-After. Health endpoints must NOT
// be wrapped — kubelet probes shouldn't see 503 when the app layer is hot.
//
// Why a custom middleware rather than chi's ThrottleBacklog: that emits a raw
// 429/503 with a plain-text body, breaking the {code, message, details} envelope
// every other endpoint uses (see .claude/rules/http.md §1). Same algorithm,
// service-shaped surface.
func Throttle(maxInFlight, backlog int, timeout time.Duration) func(http.Handler) http.Handler {
	if maxInFlight <= 0 {
		return func(next http.Handler) http.Handler { return next }
	}
	if backlog < 0 {
		backlog = 0
	}
	slots := make(chan struct{}, maxInFlight)
	queue := make(chan struct{}, maxInFlight+backlog)

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Reserve a queue slot. Full queue → reject immediately; the
			// caller would otherwise pile up on the OS-level accept queue.
			select {
			case queue <- struct{}{}:
				defer func() { <-queue }()
			default:
				rejectBusy(w, r, timeout)
				return
			}

			// Wait for an in-flight slot, bounded by timeout and request ctx.
			t := time.NewTimer(timeout)
			defer t.Stop()
			select {
			case slots <- struct{}{}:
				defer func() { <-slots }()
				next.ServeHTTP(w, r)
			case <-t.C:
				rejectBusy(w, r, timeout)
			case <-r.Context().Done():
				// Client gave up — MapError translates ctx.Canceled / DeadlineExceeded
				// into 499 / 504 with the right envelope.
				respond.MapError(r.Context(), w, r.Context().Err())
			}
		})
	}
}

// rejectBusy converts a throttle rejection into the standard error envelope.
// retryAfter is rounded up to the next second per RFC 7231 (Retry-After accepts
// integer seconds or an HTTP-date; we always use seconds for symmetry with 429).
func rejectBusy(w http.ResponseWriter, r *http.Request, retryAfter time.Duration) {
	if retryAfter < time.Second {
		retryAfter = time.Second
	}
	respond.MapError(r.Context(), w, apperr.ServiceUnavailable(
		localization.CodeServiceBusy,
		"service is at capacity, retry shortly",
		retryAfter,
	))
}
