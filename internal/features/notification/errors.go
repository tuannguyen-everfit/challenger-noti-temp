package notification

import (
	"errors"
	"time"

	"github.com/Everfit-io/go-service-template/internal/platform/apperr"
	"github.com/Everfit-io/go-service-template/internal/platform/localization"
)

// Sentinels; the exported ones are mapped to wire errors by errorMap.
var (
	// ErrInvalidTimezone is returned when `tz` is not an IANA time zone name.
	ErrInvalidTimezone = errors.New("notification: invalid timezone")
	// ErrNotFound is an unknown id or another user's notification (no existence leak).
	ErrNotFound = errors.New("notification: not found")
	// ErrChallengerUnavailable is challenger's read API failing after retries.
	ErrChallengerUnavailable = errors.New("notification: challenger unavailable")

	// errAuditRecorded is returned by AuditWriter when an entry with the same audit_key exists.
	errAuditRecorded = errors.New("notification: audit entry already recorded")
)

// challengerRetryAfter is the Retry-After on a 503 from a challenger outage.
const challengerRetryAfter = 5 * time.Second

var errorMap = []apperr.Mapping{
	{Sentinel: ErrInvalidTimezone, Build: apperr.BadRequest, Code: localization.CodeInvalidRequest, Message: "tz must be an IANA time zone"},
	{Sentinel: ErrNotFound, Build: apperr.NotFound, Code: CodeNotFound, Message: "notification not found"},
	{Sentinel: ErrChallengerUnavailable, Build: buildChallengerUnavailable, Code: CodeChallengerUnavailable, Message: "challenge service unavailable, retry shortly"},
}

func mapErr(err error) error { return apperr.MapSentinels(err, errorMap) }

// buildChallengerUnavailable fits apperr.ServiceUnavailable (which takes a Retry-After) to apperr.Builder.
func buildChallengerUnavailable(code localization.Code, msg string) *apperr.AppError {
	return apperr.ServiceUnavailable(code, msg, challengerRetryAfter)
}
