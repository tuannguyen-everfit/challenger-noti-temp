// Package logging sets up a JSON slog.Logger whose field names align with the
// OpenTelemetry Log Data Model (https://opentelemetry.io/docs/specs/otel/logs/data-model/):
//
//	slog field      OTel field              Notes
//	----------      ----------              -----
//	time            timestamp               slog default; OTel collectors rename automatically
//	msg             body                    slog default; OTel collectors rename automatically
//	severity_text   SeverityText            DEBUG|INFO|WARN|ERROR  (replaces slog "level")
//	severity_number SeverityNumber          numeric 5|9|13|17 per OTel spec
//	service.name    resource.service.name   attached once at construction
//	service.version resource.service.version
//	deployment.environment resource.deployment.environment
//
// Per-request fields (stamped by middleware):
//
//	request_id      — UUIDv4 minted by middleware.RequestID; echoed via Response Request-Id header
//	trace_id        — hex trace-id from W3C traceparent header (omitted when absent)
//	span_id         — hex span-id from W3C traceparent header (omitted when absent)
package logging

import (
	"context"
	"log/slog"
	"os"
	"strings"

	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/otel/trace"

	"github.com/Everfit-io/go-service-template/internal/platform/config"
)

// otelSeverityNumber maps slog levels to OTel SeverityNumber values.
// Spec: TRACE=1, DEBUG=5, INFO=9, WARN=13, ERROR=17.
func otelSeverityNumber(l slog.Level) int {
	switch {
	case l < slog.LevelDebug:
		return 1 // TRACE
	case l < slog.LevelInfo:
		return 5 // DEBUG
	case l < slog.LevelWarn:
		return 9 // INFO
	case l < slog.LevelError:
		return 13 // WARN
	default:
		return 17 // ERROR
	}
}

// severityLevel maps slog levels to the lowercase `level` vocabulary Datadog
// reads for log status; it does not recognise severity_text. Matches the field
// name @everfit-io/module-observability emits, so Datadog facets are shared.
func severityLevel(l slog.Level) string {
	switch {
	case l < slog.LevelInfo:
		return "debug"
	case l < slog.LevelWarn:
		return "info"
	case l < slog.LevelError:
		return "warn"
	default:
		return "error"
	}
}

type contextKey struct{}

// New creates an slog.Logger with OTel-compatible field names. Output format
// is controlled by cfg.LogFormat:
//
//	"json" (default)  — production/structured-ingestion shape. The JSON Log
//	                    Data Model is what OTel collectors / log aggregators
//	                    consume.
//	"text"            — human-readable key=value pairs on stdout, useful for
//	                    local `go run` / `make compose-logs` reading. NOT
//	                    intended for shipping to a log aggregator.
//
// Resource attributes (service.name, service.version, deployment.environment)
// are attached once at construction so every log line carries them.
func New(cfg *config.Config, version string) *slog.Logger {
	var level slog.Level
	switch strings.ToLower(cfg.LogLevel) {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}

	opts := &slog.HandlerOptions{
		Level: level,
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			// Replace the default "level" key with OTel severity fields.
			if a.Key == slog.LevelKey {
				lvl, ok := a.Value.Any().(slog.Level)
				if !ok {
					return a
				}
				// severity_number is appended separately via otelHandler; here
				// we just rename "level" → "severity_text" for OTel symmetry.
				return slog.Attr{
					Key:   "severity_text",
					Value: slog.StringValue(lvl.String()),
				}
			}
			return a
		},
	}

	var stdoutHandler slog.Handler
	switch strings.ToLower(cfg.LogFormat) {
	case "text":
		stdoutHandler = slog.NewTextHandler(os.Stdout, opts)
	default:
		stdoutHandler = slog.NewJSONHandler(os.Stdout, opts)
	}

	// Tee records to BOTH stdout AND the OTel slog bridge. The bridge writes
	// to the global LoggerProvider (configured in otelx.Setup) — a no-op
	// until OTEL_EXPORTER_OTLP_ENDPOINT is set, then ships to the collector.
	// Stdout output is preserved so local `go run` + `make compose-logs` both
	// keep working. The otelHandler wrapper around stdoutHandler still stamps
	// trace_id/span_id on the stdout side; the bridge picks them up via the
	// OTel context separately.
	//
	// DON'T pass `WithLoggerProvider(global.GetLoggerProvider())` here — that
	// captures the provider AT THIS MOMENT, which is the no-op default since
	// logging.New runs BEFORE otelx.Setup in main.go. The bridge's default
	// behavior is to resolve `global.GetLoggerProvider()` lazily at every
	// emit, so once otelx.Setup swaps the global provider, log records start
	// flowing AND get their TraceID/SpanID populated from ctx automatically.
	stdoutWrapped := &otelHandler{Handler: stdoutHandler}
	otlpHandler := otelslog.NewHandler("go-service-template")
	handler := &multiHandler{handlers: []slog.Handler{stdoutWrapped, otlpHandler}}

	log := slog.New(handler)

	return log.With(
		slog.String("service.name", "go-service-template"),
		slog.String("service.version", version),
		slog.String("deployment.environment", cfg.Env),
	)
}

// multiHandler fans out every record to multiple slog.Handlers. Used to tee
// log lines to stdout AND the OTel bridge simultaneously.
type multiHandler struct {
	handlers []slog.Handler
}

func (h *multiHandler) Enabled(ctx context.Context, level slog.Level) bool {
	for _, hh := range h.handlers {
		if hh.Enabled(ctx, level) {
			return true
		}
	}
	return false
}

func (h *multiHandler) Handle(ctx context.Context, r slog.Record) error {
	var firstErr error
	for _, hh := range h.handlers {
		// Clone so each child handler sees its own copy — Handle may consume
		// the attrs iterator (slog.Record uses a private attrs source).
		if err := hh.Handle(ctx, r.Clone()); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (h *multiHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	out := make([]slog.Handler, len(h.handlers))
	for i, hh := range h.handlers {
		out[i] = hh.WithAttrs(attrs)
	}
	return &multiHandler{handlers: out}
}

func (h *multiHandler) WithGroup(name string) slog.Handler {
	out := make([]slog.Handler, len(h.handlers))
	for i, hh := range h.handlers {
		out[i] = hh.WithGroup(name)
	}
	return &multiHandler{handlers: out}
}

// Module returns a per-package logger factory. Use in each package's log.go:
//
//	var log = logging.Module("feature.items")
//
// Then `log(ctx).Info(...)` returns the request-scoped logger with
// `module=<name>` stamped on every line. The slog.Attr is built once at
// package init, not per call.
//
// Naming convention: `<bucket>.<package>` — `feature.items`,
// `feature.watchlist`, `infra.mongox`, `platform.httpx.respond`. See
// .claude/rules/layout.md.
func Module(name string) func(context.Context) *slog.Logger {
	attr := slog.String("module", name)
	return func(ctx context.Context) *slog.Logger {
		return FromContext(ctx).With(attr)
	}
}

// otelHandler wraps a slog.Handler to inject severity_number alongside every record.
type otelHandler struct {
	slog.Handler
}

func (h *otelHandler) Handle(ctx context.Context, r slog.Record) error {
	// Prepend severity_number so it appears near severity_text.
	r2 := slog.NewRecord(r.Time, r.Level, r.Message, r.PC)
	r2.AddAttrs(
		slog.Int("severity_number", otelSeverityNumber(r.Level)),
		slog.String("level", severityLevel(r.Level)),
	)

	// Auto-stamp trace_id / span_id from the active OTel span in ctx, if any.
	// `otelhttp.NewHandler` populates this for every inbound request (server
	// span — regardless of whether the client sent `traceparent`). Background
	// goroutines opt in by wrapping work in otelx.Span. When no span is
	// active, both fields are omitted — no empty strings emitted.
	if sc := trace.SpanFromContext(ctx).SpanContext(); sc.IsValid() {
		r2.AddAttrs(
			slog.String("trace_id", sc.TraceID().String()),
			slog.String("span_id", sc.SpanID().String()),
		)
	}

	r.Attrs(func(a slog.Attr) bool {
		r2.AddAttrs(a)
		return true
	})
	return h.Handler.Handle(ctx, r2)
}

func (h *otelHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &otelHandler{Handler: h.Handler.WithAttrs(attrs)}
}

func (h *otelHandler) WithGroup(name string) slog.Handler {
	return &otelHandler{Handler: h.Handler.WithGroup(name)}
}

// ContextWithLogger stores a logger in ctx so request-scoped handlers can
// retrieve it with FromContext.
func ContextWithLogger(ctx context.Context, log *slog.Logger) context.Context {
	return context.WithValue(ctx, contextKey{}, log)
}

// FromContext returns the logger stored by ContextWithLogger, or the default
// slog logger if none is present. The returned logger is wrapped so that
// `.Info(msg)` (which would otherwise pass context.Background to Handle)
// passes THIS ctx instead — so the otelslog bridge sees the active span and
// stamps trace_id/span_id on the emitted log record.
//
// Without this wrap, every callsite would need `.InfoContext(ctx, msg)` for
// trace correlation to work. With it, `log(ctx).Info(msg)` Just Works.
func FromContext(ctx context.Context) *slog.Logger {
	base := slog.Default()
	if log, ok := ctx.Value(contextKey{}).(*slog.Logger); ok && log != nil {
		base = log
	}
	return slog.New(&ctxBound{ctx: ctx, inner: base.Handler()})
}

// ctxBound substitutes a captured ctx for whatever ctx slog passes in. slog's
// non-Context methods (Info/Warn/etc.) pass context.Background; we want the
// request-scoped ctx (with its active OTel span) instead.
type ctxBound struct {
	ctx   context.Context
	inner slog.Handler
}

func (h *ctxBound) Enabled(_ context.Context, level slog.Level) bool {
	return h.inner.Enabled(h.ctx, level) //nolint:contextcheck // intentional: substitute the bound ctx; that IS the point of this wrapper
}
func (h *ctxBound) Handle(_ context.Context, r slog.Record) error {
	return h.inner.Handle(h.ctx, r) //nolint:contextcheck // intentional: see Enabled
}
func (h *ctxBound) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &ctxBound{ctx: h.ctx, inner: h.inner.WithAttrs(attrs)}
}
func (h *ctxBound) WithGroup(name string) slog.Handler {
	return &ctxBound{ctx: h.ctx, inner: h.inner.WithGroup(name)}
}
