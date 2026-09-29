package middleware

import (
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"go.opentelemetry.io/otel/trace"

	"github.com/Everfit-io/go-service-template/internal/platform/httpx/decode"
	"github.com/Everfit-io/go-service-template/internal/platform/logging"
)

// healthPaths lists URL prefixes that are excluded from access logging to
// prevent noise from frequent probe traffic.
var healthPaths = []string{"/healthcheck", "/liveness", "/readiness", "/metrics"}

// PayloadCapture opts the access log into recording request/response payloads.
// The zero value captures nothing, which is the default everywhere — see
// config.HTTPLogConfig for the safety rails and why they exist.
type PayloadCapture struct {
	Bodies      bool
	Query       bool     // log every query param (minus QueryDeny, sensitive values redacted)
	QueryDeny   []string // query params dropped entirely
	MaxValueLen int      // longest string value kept inside a captured body
	MaxFields   int      // total nodes kept per captured body
	Headers     []string // request header allowlist
}

// limits converts the config-shaped knobs into the walker's budget.
func (c PayloadCapture) limits() captureLimits {
	return captureLimits{maxValueLen: c.MaxValueLen, maxFields: c.MaxFields}
}

// enabled reports whether any capture is configured at all, so the hot path
// skips buffering entirely when the feature is off.
func (c PayloadCapture) enabled() bool {
	return c.Bodies || c.Query || len(c.Headers) > 0
}

// AccessLog logs one record per non-probe request. The `msg` is shaped to be
// useful as a collapsed-row preview in log UIs (Grafana / Datadog / etc.):
// `METHOD path → status (Nms)`. The structured fields below give the same
// info parsed out for filtering / aggregation when the row is expanded.
// trace_id / span_id are added by logging.otelHandler from ctx automatically.
//
// capture is normally the zero value. When configured it adds redacted payload
// fields — see PayloadCapture and .claude/rules/logging.md §6.
func AccessLog(capture PayloadCapture) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			accessLog(capture, next, w, r)
		})
	}
}

func accessLog(capture PayloadCapture, next http.Handler, w http.ResponseWriter, r *http.Request) {
	for _, p := range healthPaths {
		if strings.HasPrefix(r.URL.Path, p) {
			next.ServeHTTP(w, r)
			return
		}
	}

	reqBody := readRequestBody(r, capture)

	rw := newRWCapture(w)
	if capture.Bodies {
		rw = newRWCaptureWithBody(w, captureReadLimit)
	}
	start := time.Now()
	next.ServeHTTP(rw, r)
	dur := time.Since(start)
	durMs := float64(dur.Nanoseconds()) / 1e6

	// Rename the otelhttp span to the chi route pattern AFTER routing —
	// keeps trace + metric cardinality bounded (no per-key span names).
	// trace.SpanFromContext returns the active otelhttp server span; it
	// hasn't been End()ed yet (otelhttp's defer fires after we return).
	if rc := chi.RouteContext(r.Context()); rc != nil && rc.RoutePattern() != "" {
		trace.SpanFromContext(r.Context()).SetName(r.Method + " " + rc.RoutePattern())
	}

	attrs := make([]any, 0, 10)
	attrs = append(attrs,
		slog.String("method", r.Method),
		slog.String("path", r.URL.Path),
		slog.Int("status", rw.status),
		slog.Int("bytes", rw.bytes),
		slog.Float64("duration_ms", durMs),
		slog.String("remote_ip", r.RemoteAddr),
	)
	attrs = append(attrs, captureAttrs(r, rw, reqBody, capture)...)

	logging.FromContext(r.Context()).InfoContext(r.Context(),
		fmt.Sprintf("%s %s → %d (%.1fms)", r.Method, r.URL.Path, rw.status, durMs), attrs...)
}

// captureAttrs builds the optional payload fields. Returns nil when capture is
// off, so the default access-log shape is byte-for-byte unchanged.
func captureAttrs(r *http.Request, rw *rwCapture, reqBody []byte, capture PayloadCapture) []any {
	if !capture.enabled() {
		return nil
	}

	var attrs []any
	if params := routeParams(r); len(params) > 0 {
		attrs = append(attrs, slog.Any("path_params", params))
	}
	if capture.Query {
		if q := pickExcept(r.URL.Query(), capture.QueryDeny, capture.MaxValueLen); q != nil {
			attrs = append(attrs, slog.Any("query", q))
		}
	}
	if h := pickAllowed(r.Header, capture.Headers, capture.MaxValueLen); h != nil {
		attrs = append(attrs, slog.Any("headers", h))
	}
	if !capture.Bodies {
		return attrs
	}
	lim := capture.limits()
	if v := redactBody(reqBody, r.Header.Get("Content-Type"), lim); v != nil {
		attrs = append(attrs, slog.Any("request_body", v))
	}
	if v := redactBody(rw.capturedBody(), rw.Header().Get("Content-Type"), lim); v != nil {
		attrs = append(attrs, slog.Any("response_body", v))
	}
	return attrs
}

// routeParams extracts chi's resolved path params ({id} → "68f2a…").
// Safe to log without an allowlist: these values are already present verbatim
// in the `path` field and in the span's url.path attribute.
func routeParams(r *http.Request) map[string]string {
	rc := chi.RouteContext(r.Context())
	if rc == nil {
		return nil
	}
	out := make(map[string]string, len(rc.URLParams.Keys))
	for i, k := range rc.URLParams.Keys {
		if i < len(rc.URLParams.Values) && k != "*" {
			out[k] = rc.URLParams.Values[i]
		}
	}
	return out
}

// captureReadLimit bounds how much of a payload is buffered for capture. It is
// a MEMORY bound applied while reading, distinct from MaxValueLen/MaxFields,
// which bound what reaches the log and are applied after redaction. A body has
// to be read whole before it can be parsed and redacted at all — truncating
// first would cut the JSON mid-token, leaving nothing to walk.
//
// Tied to the handler's own body cap: decode.Body rejects anything larger, so
// buffering past this point costs memory for bytes that are about to 413.
const captureReadLimit = decode.DefaultMaxBodyBytes

// readRequestBody buffers the request body so it can be logged, then restores
// r.Body so the handler still reads a complete stream.
//
// The handler must see every byte regardless of what capture keeps, so the
// buffered head is chained back in front of the unread remainder.
func readRequestBody(r *http.Request, capture PayloadCapture) []byte {
	if !capture.Bodies || r.Body == nil || r.ContentLength == 0 {
		return nil
	}
	head, err := io.ReadAll(io.LimitReader(r.Body, captureReadLimit))
	// Restore before checking err: the bytes are already off the stream either
	// way, and a handler reading a silently truncated body is worse than not
	// capturing. On error we chain back what we got and capture nothing.
	r.Body = struct {
		io.Reader
		io.Closer
	}{io.MultiReader(bytes.NewReader(head), r.Body), r.Body}
	if err != nil {
		return nil
	}
	return head
}
