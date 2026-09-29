package middleware

import (
	"context"
	"net/http"
	"time"
)

// Timeout bounds each request to d. Downstream code reading ctx will receive
// context.DeadlineExceeded once the budget is gone; respond.MapError converts
// that into a 504 REQUEST_TIMEOUT response.
//
// Apply globally as a default in NewRouter, and override per-route via chi:
//
//	r.Use(middleware.Timeout(30 * time.Second))            // global default
//	r.With(middleware.Timeout(5 * time.Second)).Get(...)   // tighter per-route
//	r.With(middleware.Timeout(2 * time.Minute)).Post(...)  // looser per-route
//
// d <= 0 returns a passthrough middleware (no timeout enforced).
func Timeout(d time.Duration) func(http.Handler) http.Handler {
	if d <= 0 {
		return func(next http.Handler) http.Handler { return next }
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx, cancel := context.WithTimeout(r.Context(), d)
			defer cancel()
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
