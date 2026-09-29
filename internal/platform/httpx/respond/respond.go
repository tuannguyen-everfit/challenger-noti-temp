// Package respond is the HTTP response helper layer. Every handler should use:
//
//   - respond.JSON(w, code, body)            — success
//   - respond.Error(w, code, locCode, msg)   — feature-specific error
//   - respond.Wrap(handler)                  — adapter so handlers can `return err`
//
// Together with the Recover middleware (catches panics) this guarantees:
//  1. Any returned error becomes a proper HTTP response via MapError, no crash.
//  2. Any panic becomes a 500 via Recover, no crash.
package respond

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/Everfit-io/go-service-template/internal/platform/localization"
)

// HeaderProvider is implemented by response types that want to attach HTTP
// headers (ETag, Cache-Control, Link, …) without changing the handler
// signature. typed.JSON checks for this and applies headers BEFORE WriteHeader.
// Implementations should return nil (not an empty map) when no headers apply,
// to keep the no-op path allocation-free.
type HeaderProvider interface {
	ResponseHeaders() http.Header
}

// JSON writes v as application/json with the given status code. Encode errors
// are logged at WARN — the response has already been partially written by then
// so retrying or 500-ing is moot.
func JSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Default().Warn("respond: encode failed", slog.String("error", err.Error()))
	}
}

// errorBody is the canonical error envelope. `code` is an i18n key in
// UPPER_SNAKE_CASE (see internal/platform/localization).
type errorBody struct {
	Code    localization.Code `json:"code"`
	Message string            `json:"message"`
}

// Error writes the canonical error envelope at the given HTTP status. The
// `code` is the stable localization key clients map to translated text.
func Error(w http.ResponseWriter, status int, code localization.Code, msg string) {
	JSON(w, status, errorBody{Code: code, Message: msg})
}

// HandlerFunc is a handler that may return an error. Use with Wrap.
//
// Convention: handlers fully resolve their own response (`respond.JSON` /
// `respond.Error`) and return nil; any non-nil return is treated as an
// unexpected error and routed through MapError (typically 500 + log).
type HandlerFunc func(w http.ResponseWriter, r *http.Request) error

// Wrap adapts a HandlerFunc into a stdlib http.HandlerFunc so it can be
// registered with chi. Returned errors flow through MapError; panics flow
// through the Recover middleware. Neither will crash the process.
func Wrap(h HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := h(w, r); err != nil {
			MapError(r.Context(), w, err)
		}
	}
}
