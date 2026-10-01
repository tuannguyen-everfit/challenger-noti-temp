package notification

import "github.com/Everfit-io/go-service-template/internal/platform/localization"

// Feature error codes; request-shape rejects use the shared localization.CodeInvalidRequest.
const (
	CodeNotFound              localization.Code = "NOTIFICATION_NOT_FOUND"
	CodeChallengerUnavailable localization.Code = "NOTIFICATION_CHALLENGER_UNAVAILABLE"
)
