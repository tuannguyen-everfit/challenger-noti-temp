package middleware

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Everfit-io/go-service-template/internal/platform/logging"
)

// captureLogs swaps slog's default logger with one writing to an in-memory
// buffer for the duration of the test.
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf
}

func TestAccessLog_EmitsLineForRegularPath(t *testing.T) {
	buf := captureLogs(t)
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("hi"))
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/things/k", nil)
	req = req.WithContext(logging.ContextWithLogger(req.Context(), slog.Default()))

	AccessLog(PayloadCapture{})(next).ServeHTTP(rec, req)

	out := buf.String()
	// msg is now a Datadog/Grafana-style preview line:
	//   `METHOD path → status (Nms)`
	// — so the collapsed row in the log UI is immediately useful. The
	// structured fields below it stay parsed for filtering / aggregation.
	if !strings.Contains(out, `"msg":"GET /api/v1/things/k → 200`) {
		t.Errorf("expected preview msg, got: %s", out)
	}
	if !strings.Contains(out, `"path":"/api/v1/things/k"`) {
		t.Errorf("expected path field in log: %s", out)
	}
	if !strings.Contains(out, `"status":200`) {
		t.Errorf("expected status field in log: %s", out)
	}
}

func TestAccessLog_SkipsHealthPaths(t *testing.T) {
	buf := captureLogs(t)
	next := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {})

	for _, p := range []string{"/healthcheck", "/liveness", "/readiness"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, p, nil)
		req = req.WithContext(logging.ContextWithLogger(req.Context(), slog.Default()))
		AccessLog(PayloadCapture{})(next).ServeHTTP(rec, req)
	}

	if strings.Contains(buf.String(), `"msg":"access"`) {
		t.Errorf("health paths should be skipped, got: %s", buf.String())
	}
}
