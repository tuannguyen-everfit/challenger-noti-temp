package typed

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// --- Test DTOs ---

type createReq struct {
	Name string `json:"name" validate:"required,max=10"`
}

type createResp struct {
	OK   bool   `json:"ok"`
	Echo string `json:"echo"`
}

type bindableReq struct {
	Key  string `validate:"required"`
	Body string `json:"body"`
}

func (b *bindableReq) Bind(r *http.Request) error {
	b.Key = r.URL.Query().Get("key")
	return nil
}

type bindErrReq struct{}

func (b *bindErrReq) Bind(_ *http.Request) error {
	return errors.New("custom bind failure")
}

type noBodyReq struct {
	ID int `query:"id" validate:"required"`
}

// --- JSON: happy path ---

func TestJSON_Success_201(t *testing.T) {
	h := JSON(http.StatusCreated, func(_ context.Context, req createReq) (createResp, error) {
		return createResp{OK: true, Echo: req.Name}, nil
	})

	body := strings.NewReader(`{"name":"hello"}`)
	req := httptest.NewRequest(http.MethodPost, "/", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rec.Code, rec.Body)
	}
	var got createResp
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if !got.OK || got.Echo != "hello" {
		t.Errorf("body = %+v", got)
	}
}

// --- JSON: body decode failure ---

func TestJSON_MalformedBody_400PayloadMalformed(t *testing.T) {
	h := JSON(http.StatusOK, func(_ context.Context, _ createReq) (createResp, error) {
		return createResp{}, nil
	})

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{not json`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (PAYLOAD_MALFORMED)", rec.Code)
	}
}

// --- JSON: validator failure ---

func TestJSON_Validation_400(t *testing.T) {
	h := JSON(http.StatusOK, func(_ context.Context, _ createReq) (createResp, error) {
		t.Fatal("handler should not be called when validation fails")
		return createResp{}, nil
	})

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"name":""}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (required)", rec.Code)
	}
}

// --- JSON: handler error → MapError ---

func TestJSON_HandlerError_500(t *testing.T) {
	h := JSON(http.StatusOK, func(_ context.Context, _ createReq) (createResp, error) {
		return createResp{}, errors.New("downstream blew up")
	})

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"name":"x"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500 (unmapped error → INTERNAL)", rec.Code)
	}
}

// --- JSON: empty-body POST (action endpoint like /publish) ---

func TestJSON_EmptyBodyPOST_NoDecodeAttempt(t *testing.T) {
	type req struct{}
	type resp struct {
		OK bool `json:"ok"`
	}
	called := false
	h := JSON(http.StatusOK, func(_ context.Context, _ req) (resp, error) {
		called = true
		return resp{OK: true}, nil
	})

	r := httptest.NewRequest(http.MethodPost, "/x/publish", http.NoBody)
	rec := httptest.NewRecorder()
	h(rec, r)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body)
	}
	if !called {
		t.Error("handler should run even without a body for POST action endpoints")
	}
}

// --- JSON: Binder interface ---

func TestJSON_Binder_PopulatesFromRequest(t *testing.T) {
	h := JSON(http.StatusOK, func(_ context.Context, req bindableReq) (createResp, error) {
		return createResp{OK: true, Echo: req.Key}, nil
	})

	r := httptest.NewRequest(http.MethodPost, "/?key=abc", strings.NewReader(`{"body":"x"}`))
	r.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h(rec, r)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body)
	}
	var got createResp
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got.Echo != "abc" {
		t.Errorf("Binder didn't populate Key; got %q", got.Echo)
	}
}

func TestJSON_BinderError_500(t *testing.T) {
	h := JSON(http.StatusOK, func(_ context.Context, _ bindErrReq) (createResp, error) {
		t.Fatal("handler should not be called when Binder fails")
		return createResp{}, nil
	})

	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{}`))
	r.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h(rec, r)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500 (unmapped bind err)", rec.Code)
	}
}

// --- JSON: AutoBind (no body, query param) ---

func TestJSON_AutoBindFromQuery(t *testing.T) {
	h := JSON(http.StatusOK, func(_ context.Context, _ noBodyReq) (createResp, error) {
		return createResp{OK: true, Echo: "id-set"}, nil
	})

	r := httptest.NewRequest(http.MethodGet, "/?id=42", nil)
	rec := httptest.NewRecorder()
	h(rec, r)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200; body=%s", rec.Code, rec.Body)
	}
}

func TestJSON_AutoBindMissingRequired_400(t *testing.T) {
	h := JSON(http.StatusOK, func(_ context.Context, _ noBodyReq) (createResp, error) {
		return createResp{}, nil
	})

	r := httptest.NewRequest(http.MethodGet, "/", nil) // missing id
	rec := httptest.NewRecorder()
	h(rec, r)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (validator: required)", rec.Code)
	}
}

// --- JSONNoContent ---

func TestJSONNoContent_Success_204(t *testing.T) {
	h := JSONNoContent(func(_ context.Context, _ createReq) error { return nil })

	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"name":"x"}`))
	r.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h(rec, r)

	if rec.Code != http.StatusNoContent {
		t.Errorf("status = %d, want 204", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("body should be empty on 204, got %q", rec.Body.String())
	}
}

func TestJSONNoContent_HandlerError_500(t *testing.T) {
	h := JSONNoContent(func(_ context.Context, _ createReq) error {
		return errors.New("boom")
	})

	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"name":"x"}`))
	r.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h(rec, r)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500 (unmapped handler error → INTERNAL)", rec.Code)
	}
}

func TestJSONNoContent_MalformedBody_400(t *testing.T) {
	h := JSONNoContent(func(_ context.Context, _ createReq) error { return nil })

	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{bad`))
	r.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h(rec, r)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestJSONNoContent_Validation_400(t *testing.T) {
	h := JSONNoContent(func(_ context.Context, _ createReq) error {
		t.Fatal("handler should not run on validation failure")
		return nil
	})

	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"name":""}`))
	r.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h(rec, r)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestJSONNoContent_Binder(t *testing.T) {
	h := JSONNoContent(func(_ context.Context, req bindableReq) error {
		if req.Key != "abc" {
			t.Errorf("Binder didn't populate Key; got %q", req.Key)
		}
		return nil
	})

	r := httptest.NewRequest(http.MethodPost, "/?key=abc", strings.NewReader(`{"body":"x"}`))
	r.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h(rec, r)

	if rec.Code != http.StatusNoContent {
		t.Errorf("status = %d, want 204; body=%s", rec.Code, rec.Body)
	}
}

// --- hasBody helper (covered via tests above, plus explicit edge cases) ---

func TestHasBody_GETHEAD_NoBody(t *testing.T) {
	// GET → handler runs even with stray body bytes ignored.
	type req struct{}
	type resp struct{}
	h := JSON(http.StatusOK, func(_ context.Context, _ req) (resp, error) { return resp{}, nil })

	r := httptest.NewRequest(http.MethodGet, "/", strings.NewReader(`ignored`))
	rec := httptest.NewRecorder()
	h(rec, r)
	if rec.Code != http.StatusOK {
		t.Errorf("GET status = %d, want 200 (body should be ignored)", rec.Code)
	}
}

// --- setFromString gaps: uint, float, parse errors ---

type wideBind struct {
	Big   uint    `query:"big"`
	Ratio float64 `query:"ratio"`
	Picky bool    `query:"picky"`
}

func TestJSON_AutoBindUintAndFloat(t *testing.T) {
	type resp struct {
		Big   uint    `json:"big"`
		Ratio float64 `json:"ratio"`
	}
	h := JSON(http.StatusOK, func(_ context.Context, req wideBind) (resp, error) {
		return resp{Big: req.Big, Ratio: req.Ratio}, nil
	})

	r := httptest.NewRequest(http.MethodGet, "/?big=42&ratio=3.14&picky=true", nil)
	rec := httptest.NewRecorder()
	h(rec, r)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body)
	}
	var got resp
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got.Big != 42 {
		t.Errorf("Big = %d, want 42", got.Big)
	}
	if got.Ratio != 3.14 {
		t.Errorf("Ratio = %v, want 3.14", got.Ratio)
	}
}

func TestJSON_AutoBindBadInt_500(t *testing.T) {
	h := JSON(http.StatusOK, func(_ context.Context, _ noBodyReq) (createResp, error) {
		t.Fatal("handler should not be called when bind fails")
		return createResp{}, nil
	})

	r := httptest.NewRequest(http.MethodGet, "/?id=not-a-number", nil)
	rec := httptest.NewRecorder()
	h(rec, r)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (AutoBind wraps as apperr.BadRequest)", rec.Code)
	}
}

func TestJSON_AutoBindBadUint_400(t *testing.T) {
	type req struct {
		N uint `query:"n" validate:"required"`
	}
	h := JSON(http.StatusOK, func(_ context.Context, _ req) (createResp, error) {
		return createResp{}, nil
	})

	r := httptest.NewRequest(http.MethodGet, "/?n=-1", nil) // negative → ParseUint fails
	rec := httptest.NewRecorder()
	h(rec, r)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (uint parse err wrapped)", rec.Code)
	}
}

func TestJSON_AutoBindBadFloat_400(t *testing.T) {
	type req struct {
		R float64 `query:"r" validate:"required"`
	}
	h := JSON(http.StatusOK, func(_ context.Context, _ req) (createResp, error) {
		return createResp{}, nil
	})

	r := httptest.NewRequest(http.MethodGet, "/?r=not-a-float", nil)
	rec := httptest.NewRecorder()
	h(rec, r)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (float parse err wrapped)", rec.Code)
	}
}

func TestJSON_AutoBindBadBool_400(t *testing.T) {
	type req struct {
		Flag bool `query:"flag" validate:"required"`
	}
	h := JSON(http.StatusOK, func(_ context.Context, _ req) (createResp, error) {
		return createResp{}, nil
	})

	r := httptest.NewRequest(http.MethodGet, "/?flag=maybe", nil)
	rec := httptest.NewRecorder()
	h(rec, r)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (bool parse err wrapped)", rec.Code)
	}
}

// --- JSONNoContent: AutoBind error path ---

func TestJSONNoContent_AutoBindError_400(t *testing.T) {
	type req struct {
		N int `query:"n" validate:"required"`
	}
	h := JSONNoContent(func(_ context.Context, _ req) error { return nil })

	r := httptest.NewRequest(http.MethodGet, "/?n=garbage", nil)
	rec := httptest.NewRecorder()
	h(rec, r)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (AutoBind wraps as apperr.BadRequest)", rec.Code)
	}
}
