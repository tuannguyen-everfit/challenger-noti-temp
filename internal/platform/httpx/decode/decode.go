// Package decode wraps the JSON-body-decode + validator dance into one call.
// Returns *apperr.AppError on every failure path so the central error mapper
// produces a uniform wire response with a stable localization code.
//
// Two flavors:
//   - JSON(w, r, dst)  — decode body + validate struct in one call. Use from
//     handlers that take raw (w, r). Most existing handlers.
//   - Body(r, dst)     — decode body only, no validate. Used by the typed
//     handler adapter at `internal/platform/httpx.JSON[Req,Resp]`, which runs
//     validation AFTER Bind so path/query fields are validated too.
package decode

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/Everfit-io/go-service-template/internal/platform/apperr"
	"github.com/Everfit-io/go-service-template/internal/platform/localization"
	"github.com/Everfit-io/go-service-template/internal/platform/validate"
)

// DefaultMaxBodyBytes is the cap for JSON used when callers don't override it.
// 32 KiB easily fits the largest struct in this service; bump per-handler when
// a specific endpoint accepts larger payloads.
const DefaultMaxBodyBytes = 32 << 10

// JSON decodes r.Body into dst with the default size cap, then runs
// validator/v10 on the result. Returns *apperr.AppError on failure.
func JSON(w http.ResponseWriter, r *http.Request, dst any) error {
	return JSONMax(w, r, dst, DefaultMaxBodyBytes)
}

// JSONMax is JSON with an explicit byte cap.
func JSONMax(w http.ResponseWriter, r *http.Request, dst any, maxBytes int64) error {
	if err := body(w, r, dst, maxBytes); err != nil {
		return err
	}
	if err := validate.Struct(dst); err != nil {
		return err // validator.ValidationErrors — MapError handles it
	}
	return nil
}

// Body decodes r.Body into dst WITHOUT running validator. Returns
// *apperr.AppError on failure. Use this from the typed handler adapter so
// validation can run on the merged (body + path + query) struct.
func Body(w http.ResponseWriter, r *http.Request, dst any) error {
	return body(w, r, dst, DefaultMaxBodyBytes)
}

func body(w http.ResponseWriter, r *http.Request, dst any, maxBytes int64) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	if err := dec.Decode(dst); err != nil {
		return classify(err, maxBytes)
	}
	if dec.More() {
		return apperr.BadRequest(localization.CodePayloadHasExtraData,
			"body must contain a single JSON object")
	}
	return nil
}

// classify maps json.Decoder errors to a granular *apperr.AppError so the
// client can show a specific localized message per cause.
func classify(err error, maxBytes int64) error {
	var (
		synErr *json.SyntaxError
		typErr *json.UnmarshalTypeError
		maxErr *http.MaxBytesError
	)
	switch {
	case errors.As(err, &synErr):
		return apperr.BadRequest(localization.CodePayloadMalformed,
			fmt.Sprintf("malformed JSON at offset %d", synErr.Offset))
	case errors.As(err, &typErr):
		return apperr.BadRequest(localization.CodePayloadFieldWrongType,
			fmt.Sprintf("field %q expects %s", typErr.Field, typErr.Type))
	case errors.As(err, &maxErr):
		return apperr.BadRequest(localization.CodePayloadTooLarge,
			fmt.Sprintf("request body exceeds %d bytes", maxBytes))
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return apperr.BadRequest(localization.CodePayloadEmpty, "request body is empty")
	case strings.HasPrefix(err.Error(), "json: unknown field "):
		return apperr.BadRequest(localization.CodePayloadUnknownField, err.Error())
	default:
		return apperr.BadRequest(localization.CodePayloadInvalid, "invalid request body")
	}
}
