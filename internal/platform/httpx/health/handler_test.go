package health

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

type mockPinger struct {
	err error
}

func (m *mockPinger) Ping(_ context.Context) error { return m.err }

var testMeta = Meta{Region: "ap-southeast-1", Name: "go-service-template", Env: "test", Version: "1.0.0"}

func TestHealthz_AllHealthy(t *testing.T) {
	h := New(&mockPinger{}, &mockPinger{}, testMeta)
	rec := httptest.NewRecorder()
	h.Healthz(rec, httptest.NewRequest(http.MethodGet, "/healthcheck", nil))

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}

	var body statusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body not JSON: %v", err)
	}
	if body.Status != statusRunning {
		t.Errorf("status = %q, want %q", body.Status, statusRunning)
	}
	if body.AppRegion != testMeta.Region || body.AppName != testMeta.Name ||
		body.AppEnv != testMeta.Env || body.AppVersion != testMeta.Version {
		t.Errorf("metadata = %+v, want it to echo %+v", body, testMeta)
	}
	if len(body.Checks) != 2 {
		t.Fatalf("checks len = %d, want 2", len(body.Checks))
	}
	if body.Checks[0].Name != "mongodb" || body.Checks[1].Name != "redis" {
		t.Errorf("check names = [%q, %q], want [mongodb, redis]", body.Checks[0].Name, body.Checks[1].Name)
	}
	for _, c := range body.Checks {
		if !c.IsHealthy || c.Message != msgConnected {
			t.Errorf("check %q = %+v, want healthy+Connected", c.Name, c)
		}
	}
}

func TestHealthz_MongoDown(t *testing.T) {
	h := New(&mockPinger{err: errors.New("mongo blip")}, &mockPinger{}, testMeta)
	rec := httptest.NewRecorder()
	h.Healthz(rec, httptest.NewRequest(http.MethodGet, "/healthcheck", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
	var body statusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body not JSON: %v", err)
	}
	if body.Status != statusDegraded {
		t.Errorf("status = %q, want %q", body.Status, statusDegraded)
	}
	if body.Checks[0].Name != "mongodb" || body.Checks[0].IsHealthy {
		t.Errorf("mongodb check = %+v, want unhealthy", body.Checks[0])
	}
	if body.Checks[0].Message != msgDisconnected {
		t.Errorf("mongodb message = %q, want %q", body.Checks[0].Message, msgDisconnected)
	}
	if !body.Checks[1].IsHealthy {
		t.Errorf("redis check = %+v, want healthy", body.Checks[1])
	}
}

func TestHealthz_RedisDown(t *testing.T) {
	h := New(&mockPinger{}, &mockPinger{err: errors.New("redis blip")}, testMeta)
	rec := httptest.NewRecorder()
	h.Healthz(rec, httptest.NewRequest(http.MethodGet, "/healthcheck", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
	var body statusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body not JSON: %v", err)
	}
	if body.Status != statusDegraded {
		t.Errorf("status = %q, want %q", body.Status, statusDegraded)
	}
	if !body.Checks[0].IsHealthy {
		t.Errorf("mongodb check = %+v, want healthy", body.Checks[0])
	}
	if body.Checks[1].Name != "redis" || body.Checks[1].IsHealthy {
		t.Errorf("redis check = %+v, want unhealthy", body.Checks[1])
	}
}

func TestLivez_OK_Before_Shutdown(t *testing.T) {
	h := New(&mockPinger{}, &mockPinger{}, testMeta)
	rec := httptest.NewRecorder()
	h.Livez(rec, httptest.NewRequest(http.MethodGet, "/liveness", nil))

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}

func TestLivez_503_After_Shutdown(t *testing.T) {
	h := New(&mockPinger{}, &mockPinger{}, testMeta)
	h.Shutdown()

	rec := httptest.NewRecorder()
	h.Livez(rec, httptest.NewRequest(http.MethodGet, "/liveness", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503 after Shutdown", rec.Code)
	}
}

func TestReadyz_BothOK(t *testing.T) {
	h := New(&mockPinger{}, &mockPinger{}, testMeta)
	rec := httptest.NewRecorder()
	h.Readyz(rec, httptest.NewRequest(http.MethodGet, "/readiness", nil))

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body not JSON: %v", err)
	}
	if body["mongo"] != "ok" || body["valkey"] != "ok" {
		t.Errorf("body = %v, want both ok", body)
	}
}

func TestReadyz_MongoDown(t *testing.T) {
	h := New(&mockPinger{err: errors.New("mongo blip")}, &mockPinger{}, testMeta)
	rec := httptest.NewRecorder()
	h.Readyz(rec, httptest.NewRequest(http.MethodGet, "/readiness", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
	var body map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body["mongo"] != "down" || body["valkey"] != "ok" {
		t.Errorf("body = %v, want mongo=down valkey=ok", body)
	}
}

func TestReadyz_ValkeyDown(t *testing.T) {
	h := New(&mockPinger{}, &mockPinger{err: errors.New("valkey blip")}, testMeta)
	rec := httptest.NewRecorder()
	h.Readyz(rec, httptest.NewRequest(http.MethodGet, "/readiness", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
	var body map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body["mongo"] != "ok" || body["valkey"] != "down" {
		t.Errorf("body = %v, want mongo=ok valkey=down", body)
	}
}

func TestReadyz_BothDown(t *testing.T) {
	h := New(&mockPinger{err: errors.New("a")}, &mockPinger{err: errors.New("b")}, testMeta)
	rec := httptest.NewRecorder()
	h.Readyz(rec, httptest.NewRequest(http.MethodGet, "/readiness", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
}
