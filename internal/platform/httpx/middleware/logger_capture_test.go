package middleware

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/Everfit-io/go-service-template/internal/platform/logging"
)

// echoJSON replies with a fixed JSON body so response capture has something
// to read, and records what the handler actually received as a request body.
func echoJSON(t *testing.T, seen *string) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("handler could not read body: %v", err)
		}
		*seen = string(b)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"access_token":"eyJhbGci","expires_in":3600}`))
	}
}

func postJSON(t *testing.T, capture PayloadCapture, body string, next http.Handler) map[string]any {
	t.Helper()
	buf := captureLogs(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login?limit=20&token=abc", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer eyJhbGci")
	req = req.WithContext(logging.ContextWithLogger(req.Context(), slog.Default()))

	AccessLog(capture)(next).ServeHTTP(httptest.NewRecorder(), req)

	var line map[string]any
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatalf("no log line: %v (%s)", err, buf.String())
	}
	return line
}

func TestAccessLog_CaptureOff_ShapeUnchanged(t *testing.T) {
	var seen string
	line := postJSON(t, PayloadCapture{}, `{"password":"Pass1234!"}`, echoJSON(t, &seen))

	for _, k := range []string{"request_body", "response_body", "headers", "query", "path_params"} {
		if _, ok := line[k]; ok {
			t.Errorf("field %q present with capture disabled: %v", k, line)
		}
	}
	if line["status"] != float64(200) {
		t.Errorf("status = %v, want 200", line["status"])
	}
}

func TestAccessLog_CaptureBodies_RedactsAndKeepsHandlerStreamIntact(t *testing.T) {
	var seen string
	body := `{"identifier":"long@everfit.io","password":"Pass1234!","type":"email"}`

	line := postJSON(t, PayloadCapture{Bodies: true, MaxValueLen: 4096, MaxFields: 200}, body, echoJSON(t, &seen))

	// The handler must still see the ORIGINAL bytes — capture reads the body
	// and must put it back, or every POST breaks.
	if seen != body {
		t.Errorf("handler received %q, want the untouched body %q", seen, body)
	}

	// The field is a nested object, so sub-fields are addressable — that is
	// the whole reason for slog.Any over a marshalled string.
	reqBody, ok := line["request_body"].(map[string]any)
	if !ok {
		t.Fatalf("request_body is %T, want a nested object", line["request_body"])
	}
	if reqBody["password"] != redactedValue || reqBody["identifier"] != redactedValue {
		t.Errorf("credentials not redacted: %v", reqBody)
	}
	if reqBody["type"] != "email" {
		t.Errorf("request_body lost useful context: %v", reqBody)
	}

	respBody, ok := line["response_body"].(map[string]any)
	if !ok {
		t.Fatalf("response_body is %T, want a nested object", line["response_body"])
	}
	if respBody["access_token"] != redactedValue {
		t.Errorf("access_token not redacted: %v", respBody)
	}
	if respBody["expires_in"] != float64(3600) {
		t.Errorf("response_body lost useful context: %v", respBody)
	}
}

func TestAccessLog_CaptureBodies_LargeBodyReachesHandlerWhole(t *testing.T) {
	var seen string
	big := `{"note":"` + strings.Repeat("a", 5000) + `"}`

	line := postJSON(t, PayloadCapture{Bodies: true, MaxValueLen: 64, MaxFields: 200}, big, echoJSON(t, &seen))

	if seen != big {
		t.Errorf("handler got %d bytes, want all %d — capture must not consume the body", len(seen), len(big))
	}
	body, ok := line["request_body"].(map[string]any)
	if !ok {
		t.Fatalf("request_body is %T, want a nested object", line["request_body"])
	}
	note, _ := body["note"].(string)
	if !strings.Contains(note, truncatedMarker) {
		t.Errorf("want the long value truncated, got %d chars", len(note))
	}
}

func TestAccessLog_HeaderAndQueryAllowlist(t *testing.T) {
	var seen string
	capture := PayloadCapture{
		Headers:     []string{"Content-Type", "Authorization"},
		Query:       true,
		QueryDeny:   []string{"token"},
		MaxValueLen: 512,
	}

	line := postJSON(t, capture, `{}`, echoJSON(t, &seen))

	headers, ok := line["headers"].(map[string]any)
	if !ok {
		t.Fatalf("headers missing: %v", line)
	}
	if headers["Content-Type"] != "application/json" {
		t.Errorf("Content-Type = %v", headers["Content-Type"])
	}
	if headers["Authorization"] != redactedValue {
		t.Errorf("Authorization = %v, want redacted", headers["Authorization"])
	}

	query, ok := line["query"].(map[string]any)
	if !ok {
		t.Fatalf("query missing: %v", line)
	}
	if query["limit"] != "20" {
		t.Errorf("limit = %v, want 20", query["limit"])
	}
	// `token` is in the URL but denied — it must not appear at all.
	if _, present := query["token"]; present {
		t.Errorf("denied query key leaked: %v", query)
	}
}

func TestAccessLog_CaptureBodies_MultipartNotEchoed(t *testing.T) {
	buf := captureLogs(t)
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusCreated)
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/media", strings.NewReader("\x89PNG\r\n\x1a\n secret image bytes"))
	req.Header.Set("Content-Type", "multipart/form-data; boundary=xyz")
	req = req.WithContext(logging.ContextWithLogger(req.Context(), slog.Default()))

	AccessLog(PayloadCapture{Bodies: true, MaxValueLen: 4096, MaxFields: 200})(next).ServeHTTP(httptest.NewRecorder(), req)

	if strings.Contains(buf.String(), "secret image bytes") {
		t.Errorf("multipart payload reached the log: %s", buf.String())
	}
}

func TestAccessLog_CapturesChiPathParams(t *testing.T) {
	buf := captureLogs(t)

	r := chi.NewRouter()
	r.Use(AccessLog(PayloadCapture{Query: true, MaxValueLen: 512}))
	r.Get("/challenges/{id}", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	req := httptest.NewRequest(http.MethodGet, "/challenges/68f2a8b3c9d4e5f6a7b8c9d0", nil)
	req = req.WithContext(logging.ContextWithLogger(req.Context(), slog.Default()))
	r.ServeHTTP(httptest.NewRecorder(), req)

	var line map[string]any
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatalf("no log line: %v (%s)", err, buf.String())
	}
	params, ok := line["path_params"].(map[string]any)
	if !ok {
		t.Fatalf("path_params missing: %v", line)
	}
	if params["id"] != "68f2a8b3c9d4e5f6a7b8c9d0" {
		t.Errorf("id = %v", params["id"])
	}
}

// errAfterReader yields n bytes, then fails — a client that disconnects
// mid-upload looks like this.
type errAfterReader struct {
	data []byte
	pos  int
}

func (r *errAfterReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.data) {
		return 0, errors.New("connection reset")
	}
	n := copy(p, r.data[r.pos:])
	r.pos += n
	return n, nil
}

func (r *errAfterReader) Close() error { return nil }

func TestAccessLog_CaptureBodies_ReadErrorStillFeedsHandler(t *testing.T) {
	captureLogs(t)

	var seen string
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		seen = string(b)
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/media", nil)
	req.Body = &errAfterReader{data: []byte(`{"a":1}`)}
	req.ContentLength = 7
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(logging.ContextWithLogger(req.Context(), slog.Default()))

	AccessLog(PayloadCapture{Bodies: true, MaxValueLen: 512, MaxFields: 200})(next).ServeHTTP(httptest.NewRecorder(), req)

	// Capture must not swallow the bytes it already pulled off the stream.
	if seen != `{"a":1}` {
		t.Errorf("handler saw %q, want the bytes capture had already read", seen)
	}
}
