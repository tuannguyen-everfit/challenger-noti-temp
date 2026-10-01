package notification

import (
	"errors"

	"github.com/Everfit-io/go-service-template/internal/platform/apperr"
	"github.com/Everfit-io/go-service-template/internal/platform/localization"
)

// ErrInvalidTimezone is returned when `tz` is not an IANA time zone name.
var ErrInvalidTimezone = errors.New("notification: invalid timezone")

var errorMap = []apperr.Mapping{
	{Sentinel: ErrInvalidTimezone, Build: apperr.BadRequest, Code: localization.CodeInvalidRequest, Message: "tz must be an IANA time zone"},
}

func mapErr(err error) error { return apperr.MapSentinels(err, errorMap) }

// errAuditRecorded is returned by AuditWriter when an entry with the same audit_key exists.
var errAuditRecorded = errors.New("notification: audit entry already recorded")
