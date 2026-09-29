package middleware

import (
	"crypto/subtle"
	"net/http"

	"github.com/Everfit-io/go-service-template/internal/platform/apperr"
	"github.com/Everfit-io/go-service-template/internal/platform/httpx/respond"
	"github.com/Everfit-io/go-service-template/internal/platform/localization"
)

// internalSecretHeader carries the shared secret on server-to-server internal
// endpoints. Bare name per RFC 6648 (no X- prefix) — see http.md §6.
const internalSecretHeader = "Internal-Secret"

const codeInternalAuthRequired localization.Code = "AUTH_REQUIRED"

// SharedSecret guards an internal (CMS → service) route with a fixed shared
// secret sent in the Internal-Secret header. A missing / empty / mismatched
// secret → 401 with the standard envelope. An empty CONFIGURED secret fails
// closed (every request 401) so a misconfigured deploy can never expose the
// route unauthenticated. Comparison is constant-time to avoid leaking the
// secret's length or prefix via response timing.
func SharedSecret(secret string) func(http.Handler) http.Handler {
	want := []byte(secret)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			got := []byte(r.Header.Get(internalSecretHeader))
			if len(want) == 0 || subtle.ConstantTimeCompare(got, want) != 1 {
				respond.MapError(r.Context(), w, apperr.Unauthorized(codeInternalAuthRequired, "internal secret required"))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
