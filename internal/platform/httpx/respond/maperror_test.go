package respond

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-playground/validator/v10"

	"github.com/Everfit-io/go-service-template/internal/platform/apperr"
	"github.com/Everfit-io/go-service-template/internal/platform/localization"
	"github.com/Everfit-io/go-service-template/internal/platform/logging"
)

// --- MapError: branch 1 — AppError ---

func TestMapError_AppError_BadRequest(t *testing.T) {
	rec := httptest.NewRecorder()
	MapError(context.Background(), rec, apperr.BadRequest(localization.Code("ITEMS_INVALID"), "bad input"))

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body["code"] != "ITEMS_INVALID" {
		t.Errorf("code = %v, want ITEMS_INVALID", body["code"])
	}
	if body["message"] != "bad input" {
		t.Errorf("message = %v, want 'bad input'", body["message"])
	}
}

func TestMapError_AppError_NotFound(t *testing.T) {
	rec := httptest.NewRecorder()
	MapError(context.Background(), rec, apperr.NotFound(localization.Code("ITEMS_NOT_FOUND"), "no such item"))
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestMapError_AppError_Conflict(t *testing.T) {
	rec := httptest.NewRecorder()
	MapError(context.Background(), rec, apperr.Conflict(localization.Code("ITEMS_KEY_TAKEN"), "exists"))
	if rec.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409", rec.Code)
	}
}

func TestMapError_AppError_Unprocessable(t *testing.T) {
	rec := httptest.NewRecorder()
	MapError(context.Background(), rec, apperr.Unprocessable(localization.Code("ITEMS_BAD_STATE"), "wrong state"))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 422", rec.Code)
	}
}

func TestMapError_AppError_RetryAfterHeader(t *testing.T) {
	rec := httptest.NewRecorder()
	MapError(context.Background(), rec, apperr.TooManyRequests(localization.Code("RATE_LIMITED"), "slow down", 30*time.Second))

	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("status = %d, want 429", rec.Code)
	}
	if h := rec.Header().Get("Retry-After"); h != "30" {
		t.Errorf("Retry-After = %q, want \"30\"", h)
	}
}

func TestMapError_AppError_NoRetryAfterHeaderWhenZero(t *testing.T) {
	rec := httptest.NewRecorder()
	MapError(context.Background(), rec, apperr.BadRequest(localization.Code("X"), "x"))
	if h := rec.Header().Get("Retry-After"); h != "" {
		t.Errorf("Retry-After should be unset for non-429, got %q", h)
	}
}

func TestMapError_AppError_CustomHeadersFlushed(t *testing.T) {
	rec := httptest.NewRecorder()
	app := apperr.Unauthorized(localization.Code("UNAUTH"), "auth required").
		WithHeader("WWW-Authenticate", `Bearer realm="api"`).
		WithHeader("Cache-Control", "no-store")
	MapError(context.Background(), rec, app)
	if got := rec.Header().Get("WWW-Authenticate"); got != `Bearer realm="api"` {
		t.Errorf("WWW-Authenticate = %q", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q", got)
	}
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

func TestMapError_AppError_ExplicitHeaderBeatsRetryAfterSugar(t *testing.T) {
	rec := httptest.NewRecorder()
	// RFC 7231 §7.1.3 allows Retry-After as integer seconds OR HTTP-date.
	// The RetryAfter sugar produces seconds; explicit WithHeader must win so
	// callers can supply an HTTP-date when they have a fixed wall-clock target.
	app := apperr.TooManyRequests(localization.Code("RATE_LIMITED"), "slow down", 5*time.Second).
		WithHeader("Retry-After", "Wed, 21 Oct 2026 07:28:00 GMT")
	MapError(context.Background(), rec, app)
	if got := rec.Header().Get("Retry-After"); got != "Wed, 21 Oct 2026 07:28:00 GMT" {
		t.Errorf("explicit Retry-After overwritten by sugar: %q", got)
	}
}

func TestMapError_AppError_LinkHeaderMultiValue(t *testing.T) {
	rec := httptest.NewRecorder()
	app := apperr.NotFound(localization.Code("NF"), "nf").
		WithHeaderAdd("Link", `</next>; rel="next"`).
		WithHeaderAdd("Link", `</prev>; rel="prev"`)
	MapError(context.Background(), rec, app)
	vs := rec.Header().Values("Link")
	if len(vs) != 2 {
		t.Fatalf("got %d Link values, want 2: %v", len(vs), vs)
	}
}

func TestMapError_AppError_WithDetails_Stringified(t *testing.T) {
	rec := httptest.NewRecorder()
	app := apperr.BadRequest(localization.Code("PAYLOAD_INVALID"), "validation").WithDetails(map[string]any{
		"name":  "required",
		"count": 42,
		"err":   errors.New("inner err"),
	})
	MapError(context.Background(), rec, app)

	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	details, ok := body["details"].(map[string]any)
	if !ok {
		t.Fatalf("details missing or wrong type; body = %v", body)
	}
	if details["name"] != "required" {
		t.Errorf("name = %v, want 'required'", details["name"])
	}
	if details["count"] != "42" {
		t.Errorf("count = %v, want '42' (Sprintf fallback)", details["count"])
	}
	if details["err"] != "inner err" {
		t.Errorf("err = %v, want 'inner err' (error.Error())", details["err"])
	}
}

func TestMapError_AppError_OmitsDetailsWhenEmpty(t *testing.T) {
	rec := httptest.NewRecorder()
	MapError(context.Background(), rec, apperr.BadRequest(localization.Code("X"), "x"))
	if strings.Contains(rec.Body.String(), "details") {
		t.Errorf("details should be omitted for empty details; body = %s", rec.Body.String())
	}
}

func TestMapError_AppError_WrappedSentinelStillMatches(t *testing.T) {
	sentinel := errors.New("inner")
	app := apperr.Conflict(localization.Code("X"), "msg").Wrap(sentinel)
	wrapped := fmt.Errorf("outer: %w", app)

	rec := httptest.NewRecorder()
	MapError(context.Background(), rec, wrapped)
	if rec.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409 (errors.As should reach AppError through fmt wrap)", rec.Code)
	}
}

// --- MapError: branch 2 — validator.ValidationErrors ---

type sampleRequest struct {
	Name  string `json:"name"  validate:"required,max=10"`
	Owner string `json:"owner" validate:"required"`
}

func TestMapError_ValidationErrors_400WithPerFieldDetails(t *testing.T) {
	v := validator.New()
	err := v.Struct(sampleRequest{Name: "", Owner: ""}) // both required → 2 violations
	if err == nil {
		t.Fatalf("expected validation error, got nil")
	}

	rec := httptest.NewRecorder()
	MapError(context.Background(), rec, err)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body["code"] != string(localization.CodeInvalidRequest) {
		t.Errorf("code = %v, want %s", body["code"], localization.CodeInvalidRequest)
	}
	details, ok := body["details"].(map[string]any)
	if !ok {
		t.Fatalf("details missing")
	}
	if details["Name"] != "required" {
		t.Errorf("Name = %v, want 'required'", details["Name"])
	}
	if details["Owner"] != "required" {
		t.Errorf("Owner = %v, want 'required'", details["Owner"])
	}
}

// --- MapError: branches 3, 4 — context errors ---

func TestMapError_DeadlineExceeded_504(t *testing.T) {
	rec := httptest.NewRecorder()
	MapError(context.Background(), rec, context.DeadlineExceeded)
	if rec.Code != http.StatusGatewayTimeout {
		t.Errorf("status = %d, want 504", rec.Code)
	}
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body["code"] != string(localization.CodeRequestTimeout) {
		t.Errorf("code = %v, want REQUEST_TIMEOUT", body["code"])
	}
}

func TestMapError_DeadlineExceeded_WrappedStillMatches(t *testing.T) {
	rec := httptest.NewRecorder()
	MapError(context.Background(), rec, fmt.Errorf("repo: %w", context.DeadlineExceeded))
	if rec.Code != http.StatusGatewayTimeout {
		t.Errorf("status = %d, want 504 (errors.Is should unwrap)", rec.Code)
	}
}

func TestMapError_Canceled_499(t *testing.T) {
	rec := httptest.NewRecorder()
	MapError(context.Background(), rec, context.Canceled)
	if rec.Code != 499 {
		t.Errorf("status = %d, want 499 (nginx convention)", rec.Code)
	}
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body["code"] != string(localization.CodeClientCanceled) {
		t.Errorf("code = %v, want CLIENT_CANCELED", body["code"])
	}
}

// --- MapError: branch 5 — default ---

func TestMapError_UnknownError_500(t *testing.T) {
	rec := httptest.NewRecorder()
	MapError(context.Background(), rec, errors.New("something unexpected"))
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body["code"] != string(localization.CodeInternal) {
		t.Errorf("code = %v, want INTERNAL_ERROR", body["code"])
	}
	if body["message"] != "internal error" {
		t.Errorf("message = %v, want 'internal error' (must NOT leak inner err string)", body["message"])
	}
}

// The 500 log line is the only place the root cause is recorded. If it doesn't
// carry the request-scoped fields, a support ticket quoting Request-Id can't
// reach it — the whole point of passing ctx into MapError.
func TestMapError_UnknownError_LogsWithRequestScope(t *testing.T) {
	var buf bytes.Buffer
	ctx := logging.ContextWithLogger(context.Background(),
		slog.New(slog.NewJSONHandler(&buf, nil)).With(slog.String("request_id", "rid-42")))

	MapError(ctx, httptest.NewRecorder(), errors.New("mongo: connection refused"))

	var line map[string]any
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatalf("no log line emitted: %v (buf=%q)", err, buf.String())
	}
	if line["request_id"] != "rid-42" {
		t.Errorf("request_id = %v, want rid-42", line["request_id"])
	}
	if line["error"] != "mongo: connection refused" {
		t.Errorf("error = %v, want the inner cause", line["error"])
	}
}

// --- MapError priority: AppError takes precedence over context.Canceled etc. ---

func TestMapError_AppErrorBeatsContextCanceled(t *testing.T) {
	app := apperr.Conflict(localization.Code("X"), "x").Wrap(context.Canceled)
	rec := httptest.NewRecorder()
	MapError(context.Background(), rec, app)
	if rec.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409 (AppError wins over ctx.Canceled in priority order)", rec.Code)
	}
}

// --- stringifyDetails: covered indirectly by AppError_WithDetails test;
//     this case targets the empty-map → nil branch directly via JSON shape.

func TestMapError_AppError_NilDetailsMap(t *testing.T) {
	// WithDetails(nil) should still omit the field.
	rec := httptest.NewRecorder()
	MapError(context.Background(), rec, apperr.BadRequest(localization.Code("X"), "x").WithDetails(nil))
	if strings.Contains(rec.Body.String(), "details") {
		t.Errorf("details should be omitted for nil details map; body = %s", rec.Body.String())
	}
}
