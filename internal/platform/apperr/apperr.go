// Package apperr defines AppError — the typed error a handler returns when it
// wants to control the HTTP response (status, localization code, optional
// details). Feature services should still return their own sentinel errors
// (errors.Is-friendly); handlers translate sentinels into AppErrors at the
// HTTP boundary.
//
// Resolution chain in respond.MapError:
//  1. errors.As(err, *AppError)         → use its fields verbatim
//  2. validator.ValidationErrors        → 400 + per-field details
//  3. http.MaxBytesError / json.* / EOF → 400 INVALID_JSON
//  4. context.DeadlineExceeded          → 504 REQUEST_TIMEOUT
//  5. context.Canceled                  → 499 CLIENT_CANCELED
//  6. default                           → 500 INTERNAL_ERROR + log
package apperr

import (
	"net/http"
	"strings"
	"time"

	"github.com/Everfit-io/go-service-template/internal/platform/localization"
)

// AppError is the typed error used to carry HTTP status + i18n code + optional
// details out of a handler. Construct it via the package functions; never
// build the struct literal in feature code — that way a new field stays
// optional everywhere.
type AppError struct {
	HTTPStatus int               // 400, 401, 403, 404, 409, 422, 429, ...
	Code       localization.Code // UPPER_SNAKE_CASE i18n key
	Message    string            // developer-facing; client uses Code for i18n
	Details    map[string]any    // per-field validation, dynamic args, etc.
	RetryAfter time.Duration     // sugar for Retry-After header on 429 / 503
	Headers    http.Header       // arbitrary response headers; see .claude/rules/headers.md
	cause      error             // wrapped error (for logs + errors.Is)
}

// Error implements the error interface.
func (e *AppError) Error() string { return e.Message }

// Unwrap returns the wrapped cause so errors.Is / errors.As reach through.
func (e *AppError) Unwrap() error { return e.cause }

// Wrap attaches a cause to the AppError and returns it (chainable).
func (e *AppError) Wrap(cause error) *AppError {
	e.cause = cause
	return e
}

// WithDetails attaches a details map (replaces any existing one).
func (e *AppError) WithDetails(d map[string]any) *AppError {
	e.Details = d
	return e
}

// WithHeader sets a response header that respond.MapError will write before
// the status line. Repeated calls with the same key replace the previous
// value (use WithHeaderAdd for multi-value headers like Link). See
// .claude/rules/headers.md for the catalog of headers worth attaching.
func (e *AppError) WithHeader(key, value string) *AppError {
	if e.Headers == nil {
		e.Headers = http.Header{}
	}
	e.Headers.Set(key, value)
	return e
}

// WithHeaderAdd appends a value to a multi-value header (Link, Vary, etc.).
// Use this instead of WithHeader when the spec allows comma-separated values.
func (e *AppError) WithHeaderAdd(key, value string) *AppError {
	if e.Headers == nil {
		e.Headers = http.Header{}
	}
	e.Headers.Add(key, value)
	return e
}

// BadRequest — 400. Client sent malformed or invalid input.
func BadRequest(code localization.Code, msg string) *AppError {
	return &AppError{HTTPStatus: http.StatusBadRequest, Code: code, Message: msg}
}

// Unauthorized — 401. No credentials / invalid credentials.
func Unauthorized(code localization.Code, msg string) *AppError {
	return &AppError{HTTPStatus: http.StatusUnauthorized, Code: code, Message: msg}
}

// Forbidden — 403. Authenticated but not allowed.
func Forbidden(code localization.Code, msg string) *AppError {
	return &AppError{HTTPStatus: http.StatusForbidden, Code: code, Message: msg}
}

// NotFound — 404.
func NotFound(code localization.Code, msg string) *AppError {
	return &AppError{HTTPStatus: http.StatusNotFound, Code: code, Message: msg}
}

// MethodNotAllowed — 405. RFC 9110 §15.5.6 REQUIRES an Allow header listing
// the methods that ARE valid on the resource. Pass them as the variadic — the
// constructor builds the comma-separated header value.
func MethodNotAllowed(code localization.Code, msg string, allowed ...string) *AppError {
	e := &AppError{HTTPStatus: http.StatusMethodNotAllowed, Code: code, Message: msg}
	if len(allowed) > 0 {
		e.Headers = http.Header{"Allow": []string{strings.Join(allowed, ", ")}}
	}
	return e
}

// Conflict — 409. Duplicate resource, state conflict.
func Conflict(code localization.Code, msg string) *AppError {
	return &AppError{HTTPStatus: http.StatusConflict, Code: code, Message: msg}
}

// Gone — 410. Resource removed permanently.
func Gone(code localization.Code, msg string) *AppError {
	return &AppError{HTTPStatus: http.StatusGone, Code: code, Message: msg}
}

// Unprocessable — 422. Syntactically valid but semantically wrong.
func Unprocessable(code localization.Code, msg string) *AppError {
	return &AppError{HTTPStatus: http.StatusUnprocessableEntity, Code: code, Message: msg}
}

// PreconditionRequired — 428 (RFC 6585 §3). Use when the server REQUIRES the
// request to be conditional (typically because the resource supports
// optimistic locking) and the client didn't send If-Match / If-None-Match.
// Compare to 412 Precondition Failed (client sent the header, value didn't
// match) and 409 Conflict (concurrent write detected at apply-time).
func PreconditionRequired(code localization.Code, msg string) *AppError {
	return &AppError{HTTPStatus: http.StatusPreconditionRequired, Code: code, Message: msg}
}

// PreconditionFailed — 412 (RFC 9110 §15.5.13). Use when the client supplied
// a conditional header (If-Match / If-None-Match / If-Unmodified-Since) and
// the precondition evaluated to false. Distinct from 428 (header missing) and
// 409 (no precondition stated; conflict detected later).
func PreconditionFailed(code localization.Code, msg string) *AppError {
	return &AppError{HTTPStatus: http.StatusPreconditionFailed, Code: code, Message: msg}
}

// TooManyRequests — 429. retryAfter is exposed via the Retry-After header by
// respond.MapError; zero or negative omits the header.
func TooManyRequests(code localization.Code, msg string, retryAfter time.Duration) *AppError {
	return &AppError{
		HTTPStatus: http.StatusTooManyRequests,
		Code:       code,
		Message:    msg,
		RetryAfter: retryAfter,
	}
}

// Internal — 500. Reserve for unexpected paths; prefer letting an unmapped
// error fall through to the default in MapError so it gets logged.
func Internal(code localization.Code, msg string) *AppError {
	return &AppError{HTTPStatus: http.StatusInternalServerError, Code: code, Message: msg}
}

// NotImplemented — 501 (RFC 9110 §15.6.2). The route is registered but the
// handler isn't built yet. Used by feature scaffolds before the endpoint logic
// lands.
func NotImplemented(code localization.Code, msg string) *AppError {
	return &AppError{HTTPStatus: http.StatusNotImplemented, Code: code, Message: msg}
}

// ServiceUnavailable — 503. retryAfter same semantics as TooManyRequests.
func ServiceUnavailable(code localization.Code, msg string, retryAfter time.Duration) *AppError {
	return &AppError{
		HTTPStatus: http.StatusServiceUnavailable,
		Code:       code,
		Message:    msg,
		RetryAfter: retryAfter,
	}
}
