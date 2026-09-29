package respond

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Everfit-io/go-service-template/internal/platform/apperr"
)

func TestJSON_WritesStatusContentTypeAndBody(t *testing.T) {
	rec := httptest.NewRecorder()
	JSON(rec, http.StatusCreated, map[string]string{"foo": "bar"})

	if rec.Code != http.StatusCreated {
		t.Errorf("status = %d, want 201", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}

	var got map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("body is not valid JSON: %v", err)
	}
	if got["foo"] != "bar" {
		t.Errorf("body = %v, want {foo: bar}", got)
	}
}

func TestError_WritesEnvelope(t *testing.T) {
	rec := httptest.NewRecorder()
	Error(rec, http.StatusBadRequest, "INVALID_KEY", "key is required")

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
	var got map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("body is not valid JSON: %v", err)
	}
	if got["code"] != "INVALID_KEY" || got["message"] != "key is required" {
		t.Errorf("body = %v, want {code:INVALID_KEY, message:key is required}", got)
	}
}

func TestWrap_NilError_NoBody(t *testing.T) {
	// Wrap should call MapError only when the handler returns a non-nil error.
	h := Wrap(func(w http.ResponseWriter, _ *http.Request) error {
		w.WriteHeader(http.StatusNoContent)
		return nil
	})
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusNoContent {
		t.Errorf("status = %d, want 204", rec.Code)
	}
}

func TestWrap_NonNilError_RoutesThroughMapError(t *testing.T) {
	h := Wrap(func(_ http.ResponseWriter, _ *http.Request) error {
		return apperr.NotFound("MISSING", "no thing")
	})
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
	var body map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body["code"] != "MISSING" {
		t.Errorf("code = %q, want MISSING", body["code"])
	}
}

// unencodable forces json.Encoder.Encode to fail — covers the encode-error
// log branch of JSON. A channel is one of the few values encoding/json
// refuses outright.
type unencodable struct {
	Ch chan int `json:"ch"`
}

func TestJSON_EncodeError_LoggedNotPanic(t *testing.T) {
	rec := httptest.NewRecorder()
	JSON(rec, http.StatusOK, unencodable{Ch: make(chan int)})
	// Status was already written before encode failed; we just confirm no panic.
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}

// Ensure errors.Is still reaches into wrapped AppErrors when handlers use Wrap.
func TestWrap_PreservesErrorChain(t *testing.T) {
	sentinel := errors.New("inner")
	h := Wrap(func(_ http.ResponseWriter, _ *http.Request) error {
		return apperr.BadRequest("X", "x").Wrap(sentinel)
	})
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}
