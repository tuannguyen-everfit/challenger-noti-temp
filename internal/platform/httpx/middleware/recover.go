package middleware

import (
	"context"
	"log/slog"
	"net/http"
	"runtime/debug"

	"github.com/Everfit-io/go-service-template/internal/platform/logging"
)

// Recover catches panics from downstream handlers, logs the stack trace, and
// returns a 500 Internal Server Error to the client.
func Recover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Pass ctx into the deferred closure explicitly so contextcheck is happy
		// and the recovery logger uses the exact request scope.
		defer func(ctx context.Context) {
			if rec := recover(); rec != nil {
				log := logging.FromContext(ctx)
				log.Error("panic recovered",
					slog.Any("panic", rec),
					slog.String("stack", string(debug.Stack())),
				)
				http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			}
		}(r.Context())
		next.ServeHTTP(w, r)
	})
}
