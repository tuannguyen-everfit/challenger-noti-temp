// Package otelx wires the OpenTelemetry SDK using the SPEC-STANDARD env vars
// — no custom SVC_OTEL_* prefix. The OTel Go SDK auto-reads most of
// these at exporter construction; we read a handful manually for fallbacks
// the Go SDK doesn't pick up by default.
//
// Two modes, chosen by `OTEL_EXPORTER_OTLP_ENDPOINT`:
//
//  1. ENDPOINT SET — full OTLP pipeline (traces + metrics + logs) exported to
//     the configured collector. Protocol picked by `OTEL_EXPORTER_OTLP_PROTOCOL`:
//     `http/protobuf` (default, port 4318) or `grpc` (port 4317). Use in any
//     environment where you want observability data shipped.
//
//  2. ENDPOINT UNSET — local-only TracerProvider. Spans are still created
//     (so otelhttp can stamp every request with a real trace_id/span_id and
//     our slog handler auto-injects those fields into every log line) but
//     nothing is exported. Zero network traffic. Use for local dev where
//     you want log↔span correlation without running a collector.
//
// Env var (read by us)            | Default
// --------------------------------|--------------------------------------------
// OTEL_EXPORTER_OTLP_ENDPOINT     | (empty → local-only spans; no export)
// OTEL_SERVICE_NAME               | "go-service-template"
// OTEL_SERVICE_VERSION            | buildinfo.Version() (fallback arg)
// OTEL_ENVIRONMENT / NODE_ENV     | cfg.Env (fallback arg)
// OTEL_TRACES_SAMPLER_ARG         | 1.0
// OTEL_METRIC_EXPORT_INTERVAL_MILLIS | 60000
//
// Env var (read by SDK exporters automatically)
// OTEL_EXPORTER_OTLP_ENDPOINT, _HEADERS, _TIMEOUT, _PROTOCOL, _INSECURE,
// _COMPRESSION, _CERTIFICATE, …
package otelx

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/log/global"
	"go.opentelemetry.io/otel/propagation"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// ShutdownFunc is the cleanup hook Setup returns. Safe to call even when OTel
// is disabled — it's a no-op in that case.
type ShutdownFunc func(ctx context.Context) error

// Setup initializes the global TracerProvider always, and the OTLP exporters
// when OTEL_EXPORTER_OTLP_ENDPOINT is set.
//
// versionFallback / envFallback are used only if OTEL_SERVICE_VERSION and
// OTEL_ENVIRONMENT (or NODE_ENV) are absent.
func Setup(ctx context.Context, envFallback, versionFallback string, log *slog.Logger) (ShutdownFunc, error) {
	res, err := resource.New(ctx,
		resource.WithFromEnv(), // picks up OTEL_SERVICE_NAME, OTEL_RESOURCE_ATTRIBUTES
		resource.WithAttributes(
			// Explicit fallbacks — only applied when env didn't set them via
			// OTEL_RESOURCE_ATTRIBUTES. attribute.String overrides on key collision,
			// but resource.New merges with env-side taking precedence for repeats.
			attribute.String("service.name", envOr("OTEL_SERVICE_NAME", "go-service-template")),
			attribute.String("service.version", envOr("OTEL_SERVICE_VERSION", versionFallback)),
			attribute.String("deployment.environment", envOrFirstNonEmpty(envFallback,
				"OTEL_ENVIRONMENT", "NODE_ENV")),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("otelx: resource: %w", err)
	}

	sampleRate := envFloat("OTEL_TRACES_SAMPLER_ARG", 1.0)
	if sampleRate < 0 {
		sampleRate = 0
	}
	if sampleRate > 1 {
		sampleRate = 1
	}

	endpoint := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	exportEnabled := endpoint != ""
	interval := time.Duration(envInt("OTEL_METRIC_EXPORT_INTERVAL_MILLIS", 60_000)) * time.Millisecond

	tp, err := buildTracerProvider(ctx, res, sampleRate, exportEnabled)
	if err != nil {
		return nil, err
	}
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	mp, err := buildMeterProvider(ctx, res, interval, exportEnabled)
	if err != nil {
		shutdownQuiet(ctx, log, tp.Shutdown, "trace")
		return nil, err
	}
	if mp != nil {
		otel.SetMeterProvider(mp)
	}

	lp, err := buildLoggerProvider(ctx, res, exportEnabled)
	if err != nil {
		shutdownQuiet(ctx, log, tp.Shutdown, "trace")
		if mp != nil {
			shutdownQuiet(ctx, log, mp.Shutdown, "meter")
		}
		return nil, err
	}
	if lp != nil {
		global.SetLoggerProvider(lp)
	}

	if exportEnabled {
		log.Info("otel enabled (traces + metrics + logs via OTLP)",
			slog.String("endpoint", endpoint),
			slog.Float64("sample_rate", sampleRate),
			slog.Duration("metric_interval", interval),
		)
	} else {
		log.Info("otel local-only mode (spans created for log correlation; no OTLP export — set OTEL_EXPORTER_OTLP_ENDPOINT to ship)")
	}

	return makeShutdown(tp, mp, lp, log), nil
}

func buildTracerProvider(ctx context.Context, res *resource.Resource, sampleRate float64, exportEnabled bool) (*sdktrace.TracerProvider, error) {
	opts := []sdktrace.TracerProviderOption{
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(sampleRate))),
	}
	if exportEnabled {
		var exp *otlptrace.Exporter
		var err error
		switch otlpProtocol() {
		case "grpc":
			exp, err = otlptracegrpc.New(ctx)
		default:
			exp, err = otlptracehttp.New(ctx)
		}
		if err != nil {
			return nil, fmt.Errorf("otelx: trace exporter: %w", err)
		}
		opts = append(opts, sdktrace.WithBatcher(exp))
	}
	return sdktrace.NewTracerProvider(opts...), nil
}

func buildMeterProvider(ctx context.Context, res *resource.Resource, interval time.Duration, exportEnabled bool) (*sdkmetric.MeterProvider, error) {
	if !exportEnabled {
		// No metrics-without-export mode: unlike traces (whose IDs we want in
		// logs even when offline), metrics have no value if not aggregated.
		return nil, nil //nolint:nilnil // intentional: nil + nil signals "skipped"
	}
	var exp sdkmetric.Exporter
	var err error
	switch otlpProtocol() {
	case "grpc":
		exp, err = otlpmetricgrpc.New(ctx)
	default:
		exp, err = otlpmetrichttp.New(ctx)
	}
	if err != nil {
		return nil, fmt.Errorf("otelx: metric exporter: %w", err)
	}
	return sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(exp, sdkmetric.WithInterval(interval))),
		sdkmetric.WithResource(res),
	), nil
}

func buildLoggerProvider(ctx context.Context, res *resource.Resource, exportEnabled bool) (*sdklog.LoggerProvider, error) {
	if !exportEnabled {
		// Without OTLP export, the otelslog bridge in logging.New will see the
		// global no-op LoggerProvider and silently drop records. Stdout output
		// is unaffected — it's a separate handler in the tee.
		return nil, nil //nolint:nilnil // intentional: nil + nil signals "skipped"
	}
	var exp sdklog.Exporter
	var err error
	switch otlpProtocol() {
	case "grpc":
		exp, err = otlploggrpc.New(ctx)
	default:
		exp, err = otlploghttp.New(ctx)
	}
	if err != nil {
		return nil, fmt.Errorf("otelx: log exporter: %w", err)
	}
	return sdklog.NewLoggerProvider(
		sdklog.WithProcessor(sdklog.NewBatchProcessor(exp)),
		sdklog.WithResource(res),
	), nil
}

func shutdownQuiet(ctx context.Context, log *slog.Logger, fn func(context.Context) error, what string) {
	if err := fn(ctx); err != nil {
		log.Warn("otelx: "+what+" shutdown error", slog.String("error", err.Error()))
	}
}

func makeShutdown(tp *sdktrace.TracerProvider, mp *sdkmetric.MeterProvider, lp *sdklog.LoggerProvider, log *slog.Logger) ShutdownFunc {
	return func(ctx context.Context) error {
		var lErr, mErr error
		if lp != nil {
			lErr = lp.Shutdown(ctx)
		}
		if mp != nil {
			mErr = mp.Shutdown(ctx)
		}
		tErr := tp.Shutdown(ctx)
		if lErr != nil {
			log.Warn("otelx: logger shutdown error", slog.String("error", lErr.Error()))
		}
		if mErr != nil {
			log.Warn("otelx: meter shutdown error", slog.String("error", mErr.Error()))
		}
		switch {
		case tErr != nil:
			return fmt.Errorf("otelx: tracer: %w", tErr)
		case mErr != nil:
			return fmt.Errorf("otelx: meter: %w", mErr)
		case lErr != nil:
			return fmt.Errorf("otelx: logger: %w", lErr)
		}
		return nil
	}
}

// otlpProtocol returns the OTLP wire protocol — "grpc" or "http/protobuf".
// Read from OTEL_EXPORTER_OTLP_PROTOCOL (spec-standard); default http/protobuf.
func otlpProtocol() string {
	return envOr("OTEL_EXPORTER_OTLP_PROTOCOL", "http/protobuf")
}

// envOr returns os.Getenv(key) or fallback when empty.
func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// envOrFirstNonEmpty returns the first non-empty env var from keys, falling
// back to fallback. Use for "OTEL_ENVIRONMENT or NODE_ENV" style lookups.
func envOrFirstNonEmpty(fallback string, keys ...string) string {
	for _, k := range keys {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	return fallback
}

func envFloat(key string, fallback float64) float64 {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return fallback
	}
	return f
}

func envInt(key string, fallback int) int {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	i, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	return i
}
