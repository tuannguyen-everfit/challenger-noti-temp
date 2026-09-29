package middleware

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/Everfit-io/go-service-template/internal/platform/apperr"
	"github.com/Everfit-io/go-service-template/internal/platform/httpx/respond"
	"github.com/Everfit-io/go-service-template/internal/platform/localization"
)

// AccessVerifier is implemented by the JWT signer; only VerifyAccess is needed
// at this layer.
type AccessVerifier interface {
	VerifyAccess(token string) (userID string, err error)
}

type userIDKey struct{}

// UserIDFromContext returns the authenticated user_id stamped by BearerAuth,
// or "" when the request didn't pass through the middleware.
func UserIDFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(userIDKey{}).(string); ok {
		return v
	}
	return ""
}

// WithUserID stamps an authenticated user_id on ctx — used by tests to bypass
// BearerAuth without needing a valid JWT.
func WithUserID(ctx context.Context, userID string) context.Context {
	return context.WithValue(ctx, userIDKey{}, userID)
}

// BearerAuth wraps next with Bearer-token verification. Failures return 401
// with the standard envelope; success stamps the resolved user_id on the
// request context (read via UserIDFromContext).
//
// Surface:
//
//	Authorization missing / wrong scheme → 401 AUTH_TOKEN_MISSING
//	Token invalid / signature mismatch   → 401 AUTH_TOKEN_INVALID
//	Token expired                        → 401 AUTH_TOKEN_EXPIRED
func BearerAuth(v AccessVerifier) func(http.Handler) http.Handler {
	if v == nil {
		// Defensive — should never happen in prod wiring. Make every request fail
		// closed rather than silently letting it through.
		return func(_ http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				respond.MapError(r.Context(), w, apperr.Internal(localization.CodeInternal, "auth verifier not configured"))
			})
		}
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hdr := r.Header.Get("Authorization")
			if hdr == "" {
				respond.MapError(r.Context(), w, apperr.Unauthorized(codeTokenMissing, "Authorization header required").
					WithHeader("WWW-Authenticate", `Bearer realm="api"`))
				return
			}
			const prefix = "Bearer "
			if !strings.HasPrefix(hdr, prefix) {
				respond.MapError(r.Context(), w, apperr.Unauthorized(codeTokenMissing, "Authorization must be Bearer").
					WithHeader("WWW-Authenticate", `Bearer realm="api"`))
				return
			}
			token := strings.TrimSpace(hdr[len(prefix):])
			userID, err := v.VerifyAccess(token)
			if err != nil {
				code := codeTokenInvalid
				msg := "Token invalid."
				if errors.Is(err, errExpired) {
					code = codeTokenExpired
					msg = "Token expired."
				}
				respond.MapError(r.Context(), w, apperr.Unauthorized(code, msg).
					WithHeader("WWW-Authenticate", `Bearer realm="api", error="invalid_token"`))
				return
			}
			ctx := context.WithValue(r.Context(), userIDKey{}, userID)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// Sentinel for matching authtoken.ErrExpired without an import cycle.
// Bound at wire time via SetExpiredSentinel — auth-feature wiring calls it.
var errExpired error = errors.New("middleware: token expired (placeholder)")

// SetExpiredSentinel rebinds errExpired to the concrete sentinel exported by
// platform/authtoken. Wired once at app.go startup so the middleware can use
// errors.Is without importing authtoken (which would import middleware → cycle).
func SetExpiredSentinel(err error) { errExpired = err }

// Codes localized at the auth feature; middleware uses raw localization.Code
// values to avoid coupling to feature/auth (which depends on http middleware).
const (
	codeTokenMissing localization.Code = "AUTH_TOKEN_MISSING" //nolint:gosec // G101 false positive
	codeTokenInvalid localization.Code = "AUTH_TOKEN_INVALID" //nolint:gosec // G101 false positive
	codeTokenExpired localization.Code = "AUTH_TOKEN_EXPIRED" //nolint:gosec // G101 false positive
)
