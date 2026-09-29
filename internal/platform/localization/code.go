// Package localization defines the stable i18n code type used across the
// service. Codes are UPPER_SNAKE_CASE strings clients map to translated text;
// they MUST be stable across releases — renaming a code is a wire break.
//
// Per-feature codes live in `internal/<feature>/localization.go` and import
// this package. Cross-cutting codes (validation, timeout, internal error)
// live here.
package localization

// Code is a UPPER_SNAKE_CASE i18n key. Mobile/web clients translate from this
// rather than from the human-readable message. Format: {MODULE}_{ACTION}_{DETAIL}.
type Code string

// String implements fmt.Stringer and keeps JSON encoding identical to a plain
// string. (Code is `type Code string`, so encoding/json treats it as a string
// natively — this method is for log + fmt.Printf usage.)
func (c Code) String() string { return string(c) }

// Cross-cutting codes — every feature can emit these without redefining them.
const (
	CodeInternal            Code = "INTERNAL_ERROR"
	CodeInvalidRequest      Code = "INVALID_REQUEST"
	CodeRequestTimeout      Code = "REQUEST_TIMEOUT"
	CodeClientCanceled      Code = "CLIENT_CANCELED"
	CodeRateLimited         Code = "RATE_LIMITED"
	CodeServiceBusy         Code = "SERVICE_BUSY" // edge throttle rejected the request — see middleware.Throttle
	CodeUnauthorized        Code = "UNAUTHORIZED"
	CodeForbidden           Code = "FORBIDDEN"
	CodeRouteNotFound       Code = "ROUTE_NOT_FOUND"      // chi NotFound fallback — no handler for this URL
	CodeMethodNotAllowed    Code = "METHOD_NOT_ALLOWED"   // chi MethodNotAllowed fallback — wrong verb on existing route
	CodePreconditionMissing Code = "PRECONDITION_MISSING" // 428: server requires If-Match / If-None-Match but client didn't send it
	CodePreconditionFailed  Code = "PRECONDITION_FAILED"  // 412: client sent If-Match / If-None-Match; value didn't satisfy the precondition
	CodeNotImplemented      Code = "NOT_IMPLEMENTED"      // 501: route exists but the handler isn't built yet

	// JSON-body decode codes — granular so clients can localize specific
	// "your JSON is wrong" cases to specific user messages.
	CodePayloadMalformed      Code = "PAYLOAD_MALFORMED"        // json.SyntaxError
	CodePayloadFieldWrongType Code = "PAYLOAD_FIELD_WRONG_TYPE" // json.UnmarshalTypeError
	CodePayloadTooLarge       Code = "PAYLOAD_TOO_LARGE"        // http.MaxBytesError
	CodePayloadEmpty          Code = "PAYLOAD_EMPTY"            // io.EOF
	CodePayloadUnknownField   Code = "PAYLOAD_UNKNOWN_FIELD"    // DisallowUnknownFields
	CodePayloadHasExtraData   Code = "PAYLOAD_HAS_EXTRA_DATA"   // dec.More()
	CodePayloadInvalid        Code = "PAYLOAD_INVALID"          // catch-all
)
