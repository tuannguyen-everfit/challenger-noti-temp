package decode

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-playground/validator/v10"
)

type sample struct {
	Name string `json:"name" validate:"required,max=10"`
}

func TestJSON_Success(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"name":"ok"}`))
	rec := httptest.NewRecorder()
	var got sample
	if err := JSON(rec, req, &got); err != nil {
		t.Fatalf("JSON: %v", err)
	}
	if got.Name != "ok" {
		t.Errorf("name = %q", got.Name)
	}
}

func TestJSON_UnknownField(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"name":"ok","extra":true}`))
	rec := httptest.NewRecorder()
	var got sample
	err := JSON(rec, req, &got)
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Errorf("expected unknown-field error, got %v", err)
	}
}

func TestJSON_ValidationFailure(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"name":""}`))
	rec := httptest.NewRecorder()
	var got sample
	err := JSON(rec, req, &got)
	var vErrs validator.ValidationErrors
	if !errors.As(err, &vErrs) {
		t.Fatalf("expected validator.ValidationErrors, got %T: %v", err, err)
	}
}

func TestJSONMax_BodyTooLarge(t *testing.T) {
	big := strings.Repeat("x", 100)
	body := `{"name":"` + big + `"}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	rec := httptest.NewRecorder()
	var got sample
	err := JSONMax(rec, req, &got, 16) // cap below body size
	if err == nil {
		t.Fatal("expected size-cap error")
	}
	_ = json.Marshaler(nil) // keep import in case future tests need it
}

func TestBody_DecodesWithoutValidation(t *testing.T) {
	// Body skips the validator; it's the entry point typed.JSON uses so the
	// merged (body + path + query) struct can be validated together at the end.
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"name":"ok"}`))
	rec := httptest.NewRecorder()
	var got sample
	if err := Body(rec, req, &got); err != nil {
		t.Fatalf("Body err: %v", err)
	}
	if got.Name != "ok" {
		t.Errorf("Name = %q", got.Name)
	}
}

func TestJSON_MalformedJSON(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"name":}`))
	rec := httptest.NewRecorder()
	var got sample
	if err := JSON(rec, req, &got); err == nil {
		t.Fatal("expected malformed-JSON error")
	}
}

func TestJSON_WrongType(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"name":42}`))
	rec := httptest.NewRecorder()
	var got sample
	if err := JSON(rec, req, &got); err == nil {
		t.Fatal("expected wrong-type error")
	}
}

func TestJSON_EmptyBody(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(""))
	rec := httptest.NewRecorder()
	var got sample
	if err := JSON(rec, req, &got); err == nil {
		t.Fatal("expected empty-body error")
	}
}

func TestJSON_HasExtraData(t *testing.T) {
	// Two JSON objects in one body — second one trips dec.More().
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"name":"ok"}{"more":1}`))
	rec := httptest.NewRecorder()
	var got sample
	if err := JSON(rec, req, &got); err == nil {
		t.Fatal("expected extra-data error")
	}
}

func TestClassify_UnknownError_FallsToPayloadInvalid(t *testing.T) {
	// Plain errors.New value doesn't match any errors.As/Is branch in
	// classify, exercising the default arm.
	if err := classify(errors.New("totally random decode failure"), 1024); err == nil {
		t.Fatal("expected non-nil error from default branch")
	}
}
