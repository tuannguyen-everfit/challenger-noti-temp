package apperr

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/Everfit-io/go-service-template/internal/platform/localization"
)

var (
	errSentNotFound  = errors.New("test: not found")
	errSentConflict  = errors.New("test: conflict")
	errSentBadState  = errors.New("test: bad state")
	errSentUnmatched = errors.New("test: not in table")
)

func testTable() []Mapping {
	return []Mapping{
		{errSentNotFound, NotFound, localization.Code("TEST_NOT_FOUND"), "no entity"},
		{errSentConflict, Conflict, localization.Code("TEST_CONFLICT"), "already exists"},
		{errSentBadState, Unprocessable, localization.Code("TEST_BAD_STATE"), "wrong state"},
	}
}

func TestMapSentinels_NilReturnsNil(t *testing.T) {
	if got := MapSentinels(nil, testTable()); got != nil {
		t.Errorf("MapSentinels(nil) = %v, want nil", got)
	}
}

func TestMapSentinels_MatchProducesTypedAppError(t *testing.T) {
	got := MapSentinels(errSentNotFound, testTable())

	var app *AppError
	if !errors.As(got, &app) {
		t.Fatalf("got %T, want *AppError", got)
	}
	if app.HTTPStatus != http.StatusNotFound {
		t.Errorf("status = %d, want 404", app.HTTPStatus)
	}
	if app.Code != "TEST_NOT_FOUND" {
		t.Errorf("code = %q, want TEST_NOT_FOUND", app.Code)
	}
	if app.Message != "no entity" {
		t.Errorf("message = %q, want %q", app.Message, "no entity")
	}
}

func TestMapSentinels_WrappedSentinelStillMatches(t *testing.T) {
	wrapped := fmt.Errorf("repo: lookup failed: %w", errSentConflict)
	got := MapSentinels(wrapped, testTable())

	var app *AppError
	if !errors.As(got, &app) {
		t.Fatalf("got %T, want *AppError", got)
	}
	if app.HTTPStatus != http.StatusConflict {
		t.Errorf("status = %d, want 409", app.HTTPStatus)
	}
	// Original cause must remain reachable via errors.Is so logs + alerting
	// can still classify by sentinel after mapping.
	if !errors.Is(app, errSentConflict) {
		t.Error("wrapped AppError should still match errSentConflict via errors.Is")
	}
}

func TestMapSentinels_UnmatchedPassesThrough(t *testing.T) {
	got := MapSentinels(errSentUnmatched, testTable())
	if !errors.Is(got, errSentUnmatched) {
		t.Errorf("passthrough broken: got %v, want chain to %v", got, errSentUnmatched)
	}
	// Stronger check than errors.Is alone: result must not be a wrapped
	// AppError — passthrough means "return the input unchanged."
	var app *AppError
	if errors.As(got, &app) {
		t.Error("unmatched err should NOT be wrapped as AppError")
	}
}

func TestMapSentinels_FirstMatchWins(t *testing.T) {
	// Two rows pointing at the same sentinel — first row should win.
	dupTable := []Mapping{
		{errSentNotFound, NotFound, "FIRST", "first wins"},
		{errSentNotFound, Conflict, "SECOND", "second loses"},
	}
	got := MapSentinels(errSentNotFound, dupTable)

	var app *AppError
	if !errors.As(got, &app) || app.Code != "FIRST" {
		t.Errorf("got code %q, want FIRST", app.Code)
	}
}
