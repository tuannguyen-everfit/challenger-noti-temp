package respond

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/go-playground/validator/v10"

	"github.com/Everfit-io/go-service-template/internal/platform/apperr"
	"github.com/Everfit-io/go-service-template/internal/platform/localization"
	"github.com/Everfit-io/go-service-template/internal/platform/logging"
)

// errorWithDetails extends errorBody with a per-field details map used for
// validation errors. `omitempty` keeps simple errors as 2-field envelopes.
type errorWithDetails struct {
	Code    localization.Code `json:"code"`
	Message string            `json:"message"`
	Details map[string]string `json:"details,omitempty"`
}

// MapError is the central error → HTTP mapper. Detection order:
//  1. *apperr.AppError              — verbatim status / code / details / Retry-After
//  2. validator.ValidationErrors    — 400 INVALID_REQUEST + per-field details
//  3. context.DeadlineExceeded      — 504 REQUEST_TIMEOUT
//  4. context.Canceled              — 499 CLIENT_CANCELED
//  5. default                       — 500 INTERNAL_ERROR (and log)
//
// JSON-body errors are no longer matched here — `decode.Body` wraps them as
// *apperr.AppError with granular PAYLOAD_* codes (caught by branch 1).
//
// ctx carries the request-scoped logger so the branch-5 log line lands with
// request_id / trace_id attached. Without it the one line naming the root
// cause of a 500 is unlinkable to the request that produced it.
func MapError(ctx context.Context, w http.ResponseWriter, err error) {
	// 1. Typed AppError wins (includes the PAYLOAD_* codes from decode).
	var app *apperr.AppError
	if errors.As(err, &app) {
		// Headers first — must be set BEFORE WriteHeader (inside JSON below).
		// Explicit Headers map wins over RetryAfter sugar when both are set.
		for k, vs := range app.Headers {
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
		if app.RetryAfter > 0 && w.Header().Get("Retry-After") == "" {
			w.Header().Set("Retry-After", strconv.Itoa(int(app.RetryAfter.Seconds())))
		}
		JSON(w, app.HTTPStatus, errorWithDetails{
			Code:    app.Code,
			Message: app.Message,
			Details: stringifyDetails(app.Details),
		})
		return
	}

	// 2. Validator errors — 400 + per-field details.
	var vErrs validator.ValidationErrors
	if errors.As(err, &vErrs) {
		details := make(map[string]string, len(vErrs))
		for _, fe := range vErrs {
			details[fe.Field()] = fe.Tag()
		}
		JSON(w, http.StatusBadRequest, errorWithDetails{
			Code:    localization.CodeInvalidRequest,
			Message: "request validation failed",
			Details: details,
		})
		return
	}

	if errors.Is(err, context.DeadlineExceeded) {
		Error(w, http.StatusGatewayTimeout, localization.CodeRequestTimeout, "request timed out")
		return
	}
	if errors.Is(err, context.Canceled) {
		Error(w, 499, localization.CodeClientCanceled, "client closed request")
		return
	}

	logging.FromContext(ctx).ErrorContext(ctx, "unmapped error", slog.String("error", err.Error()))
	Error(w, http.StatusInternalServerError, localization.CodeInternal, "internal error")
}

// stringifyDetails converts an arbitrary detail map into a string-valued map
// so it serializes consistently with validator-error responses.
func stringifyDetails(d map[string]any) map[string]string {
	if len(d) == 0 {
		return nil
	}
	out := make(map[string]string, len(d))
	for k, v := range d {
		switch tv := v.(type) {
		case string:
			out[k] = tv
		case error:
			out[k] = tv.Error()
		default:
			out[k] = fmt.Sprintf("%v", v)
		}
	}
	return out
}
