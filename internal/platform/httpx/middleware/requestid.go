package middleware

import (
	"context"
	"net/http"

	"github.com/google/uuid"

	"github.com/Everfit-io/go-service-template/internal/platform/logging"
)

// headerRequestID uses the BARE name per RFC 6648 (the `X-` prefix is
// deprecated for new headers). See .claude/rules/http.md §6 + headers.md §3.
const headerRequestID = "Request-Id"

type requestIDKey struct{}

// RequestIDFromContext returns the id minted by RequestID, or "" outside a request.
func RequestIDFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(requestIDKey{}).(string); ok {
		return v
	}
	return ""
}

// WithRequestID stamps a request id on ctx; RequestID uses it, tests call it directly.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey{}, id)
}

// RequestID mints a UUIDv4 for each request, echoes it in the response
// header, and injects both the value and a child logger into the request
// context.
//
// Server-only generation: this service's clients (mobile, web, partner
// services) never send Request-Id inbound — they read it FROM the response
// header for bug reports and support tickets. If a future upstream gateway
// or LB starts injecting Request-Id, add a read-header fallback before the
// uuid.New() call to honor it.
//
// Trace context (trace_id / span_id) is NOT handled here — `otelhttp.NewHandler`
// in router.go populates the OTel context for every request, and the logging
// package's slog handler auto-stamps trace_id/span_id on every log record
// from the active span. See `internal/platform/logging/logger.go::otelHandler`.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rid := uuid.New().String()
		w.Header().Set(headerRequestID, rid)

		log := logging.FromContext(r.Context()).With("request_id", rid)
		ctx := WithRequestID(logging.ContextWithLogger(r.Context(), log), rid)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
