package apperr

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/Everfit-io/go-service-template/internal/platform/localization"
)

func TestConstructors_SetExpectedStatus(t *testing.T) {
	cases := []struct {
		name string
		err  *AppError
		want int
	}{
		{"BadRequest", BadRequest("X", "x"), http.StatusBadRequest},
		{"Unauthorized", Unauthorized("X", "x"), http.StatusUnauthorized},
		{"Forbidden", Forbidden("X", "x"), http.StatusForbidden},
		{"NotFound", NotFound("X", "x"), http.StatusNotFound},
		{"Conflict", Conflict("X", "x"), http.StatusConflict},
		{"Gone", Gone("X", "x"), http.StatusGone},
		{"Unprocessable", Unprocessable("X", "x"), http.StatusUnprocessableEntity},
		{"TooManyRequests", TooManyRequests("X", "x", time.Second), http.StatusTooManyRequests},
		{"Internal", Internal("X", "x"), http.StatusInternalServerError},
		{"ServiceUnavailable", ServiceUnavailable("X", "x", 0), http.StatusServiceUnavailable},
	}
	for _, c := range cases {
		if c.err.HTTPStatus != c.want {
			t.Errorf("%s: status = %d, want %d", c.name, c.err.HTTPStatus, c.want)
		}
	}
}

func TestWrap_PreservesCauseForErrorsIs(t *testing.T) {
	cause := errors.New("root")
	app := NotFound(localization.Code("NF"), "not there").Wrap(cause)

	if !errors.Is(app, cause) {
		t.Error("errors.Is should reach the wrapped cause")
	}
	if !errors.Is(app.Unwrap(), cause) {
		t.Error("Unwrap should return the cause")
	}
}

func TestWithDetails_ReplacesMap(t *testing.T) {
	app := BadRequest("X", "x").WithDetails(map[string]any{"field": "bad"})
	if app.Details["field"] != "bad" {
		t.Errorf("details = %v", app.Details)
	}
}

func TestWithHeader_SetsAndReplaces(t *testing.T) {
	app := Unauthorized("U", "u").
		WithHeader("WWW-Authenticate", `Bearer realm="api"`).
		WithHeader("WWW-Authenticate", `Bearer realm="api", error="invalid_token"`)
	if got := app.Headers.Get("WWW-Authenticate"); got != `Bearer realm="api", error="invalid_token"` {
		t.Errorf("WithHeader did not replace: %q", got)
	}
}

func TestWithHeaderAdd_AppendsMultiValue(t *testing.T) {
	app := NotFound("NF", "nf").
		WithHeaderAdd("Link", `</next>; rel="next"`).
		WithHeaderAdd("Link", `</prev>; rel="prev"`)
	vs := app.Headers.Values("Link")
	if len(vs) != 2 {
		t.Fatalf("got %d Link values, want 2: %v", len(vs), vs)
	}
}

func TestMethodNotAllowed_SetsAllowHeader(t *testing.T) {
	app := MethodNotAllowed("MNA", "wrong verb", "GET", "POST", "PATCH")
	if app.HTTPStatus != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", app.HTTPStatus)
	}
	if got := app.Headers.Get("Allow"); got != "GET, POST, PATCH" {
		t.Errorf("Allow = %q, want \"GET, POST, PATCH\"", got)
	}
}

func TestMethodNotAllowed_NoMethods_NoAllowHeader(t *testing.T) {
	// Caller might construct a 405 without knowing the allowed set (e.g. when
	// the router didn't pre-populate it). Don't emit an empty Allow header.
	app := MethodNotAllowed("MNA", "wrong verb")
	if app.Headers != nil {
		t.Errorf("Headers should be nil when no methods supplied, got %v", app.Headers)
	}
}

func TestWithHeader_NilHeadersOnConstruct(t *testing.T) {
	// Headers stays nil until WithHeader is called — keeps zero-allocation path
	// for the common no-header case.
	if app := BadRequest("X", "x"); app.Headers != nil {
		t.Errorf("Headers should be nil on construct, got %v", app.Headers)
	}
}

func TestError_ReturnsMessage(t *testing.T) {
	app := BadRequest("X", "bad input")
	if got := app.Error(); got != "bad input" {
		t.Errorf("Error() = %q, want %q", got, "bad input")
	}
}

func TestPreconditionRequired_Status428(t *testing.T) {
	app := PreconditionRequired("PRECONDITION_MISSING", "If-Match required")
	if app.HTTPStatus != http.StatusPreconditionRequired {
		t.Errorf("status = %d, want 428", app.HTTPStatus)
	}
	if app.Code != "PRECONDITION_MISSING" {
		t.Errorf("code = %q", app.Code)
	}
}

func TestPreconditionFailed_Status412(t *testing.T) {
	app := PreconditionFailed("PRECONDITION_FAILED", "If-Match mismatch")
	if app.HTTPStatus != http.StatusPreconditionFailed {
		t.Errorf("status = %d, want 412", app.HTTPStatus)
	}
}

func TestUnprocessable_Status422(t *testing.T) {
	app := Unprocessable("UNP", "semantic error")
	if app.HTTPStatus != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 422", app.HTTPStatus)
	}
}

func TestGone_Status410(t *testing.T) {
	app := Gone("GONE", "resource gone")
	if app.HTTPStatus != http.StatusGone {
		t.Errorf("status = %d, want 410", app.HTTPStatus)
	}
}

func TestForbidden_Status403(t *testing.T) {
	app := Forbidden("FORBID", "no")
	if app.HTTPStatus != http.StatusForbidden {
		t.Errorf("status = %d, want 403", app.HTTPStatus)
	}
}

func TestInternal_Status500(t *testing.T) {
	app := Internal("INT", "boom")
	if app.HTTPStatus != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", app.HTTPStatus)
	}
}

func TestErrorsAs_FindsAppError(t *testing.T) {
	app := NotFound("NF", "missing")
	var target *AppError
	if !errors.As(error(app), &target) {
		t.Fatal("errors.As should find *AppError")
	}
	if target.HTTPStatus != http.StatusNotFound {
		t.Errorf("status = %d", target.HTTPStatus)
	}
}
