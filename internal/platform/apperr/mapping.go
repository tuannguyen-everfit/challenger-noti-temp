package apperr

import (
	"errors"

	"github.com/Everfit-io/go-service-template/internal/platform/localization"
)

// Builder is the shared signature of NotFound, Conflict, BadRequest, etc.
// MapSentinels uses it so a Mapping row can reference any of the 4xx/5xx
// constructors uniformly. TooManyRequests / ServiceUnavailable take an
// extra retry-after duration and don't fit this signature — callers that
// need them should produce an AppError directly.
type Builder func(localization.Code, string) *AppError

// Mapping pairs a domain sentinel with the AppError it becomes at the HTTP
// boundary. Used as a row in a per-feature error table; see
// `.claude/rules/http.md` §2 for the per-feature pattern.
type Mapping struct {
	Sentinel error
	Build    Builder           // e.g. apperr.NotFound, apperr.Conflict
	Code     localization.Code // i18n key clients use to render localized text
	Message  string            // developer-facing fallback message
}

// MapSentinels returns the first AppError that matches err in table (via
// errors.Is, so wrapped sentinels still resolve). Returns nil for nil; returns
// err unchanged when nothing matches — the central respond.MapError then
// classifies it (timeouts, ctx canceled, JSON decode, or 500 default).
//
// Per-feature usage:
//
//	var errorMap = []apperr.Mapping{
//	    {ErrNotFound, apperr.NotFound,      CodeNotFound,    "no foo for id"},
//	    {ErrTaken,    apperr.Conflict,      CodeFooTaken,    "foo already exists"},
//	    {ErrBadState, apperr.Unprocessable, CodeFooBadState, "foo not in valid state"},
//	}
//
//	func mapErr(err error) error { return apperr.MapSentinels(err, errorMap) }
func MapSentinels(err error, table []Mapping) error {
	if err == nil {
		return nil
	}
	for _, m := range table {
		if errors.Is(err, m.Sentinel) {
			return m.Build(m.Code, m.Message).Wrap(err)
		}
	}
	return err
}
