package otelx

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace"
)

// tracerName is the OTel instrumentation scope. Single tracer for the whole
// service — simpler than per-package tracers and adequate for app-level
// spans. The producing feature is recorded as a span attribute or the span
// name's prefix (e.g. "items.Service.findByKey").
const tracerName = "go-service-template"

// Span starts a child span on the global tracer and returns the new ctx
// (carrying the span) plus an `end` closure. Designed for the common
// "time this block" pattern:
//
//	ctx, end := otelx.Span(ctx, "items.repo.FindByKey")
//	defer end()
//	return r.coll.FindOne(ctx, ...)
//
// When OTel is disabled (OTEL_EXPORTER_OTLP_ENDPOINT unset), the global noop
// TracerProvider makes this a near-zero-overhead pass-through — safe to call
// from hot paths.
//
// Naming convention: dot-separated, identifies BOTH the feature and the
// operation. Good: "items.Service.Publish", "watchlist.repo.MarkNotified".
// Bad: "FindByKey" (which feature?), "do work" (what operation?).
//
// Span attributes can be added via the returned ctx with trace.SpanFromContext:
//
//	ctx, end := otelx.Span(ctx, "items.repo.FindByKey")
//	defer end()
//	trace.SpanFromContext(ctx).SetAttributes(attribute.String("item.key", key))
func Span(ctx context.Context, name string) (context.Context, func()) {
	ctx, span := otel.Tracer(tracerName).Start(ctx, name)
	return ctx, func() { span.End() }
}

// SpanFromContext is a re-export of trace.SpanFromContext so callers don't
// need a second OTel import just to add attributes to an open span.
func SpanFromContext(ctx context.Context) trace.Span {
	return trace.SpanFromContext(ctx)
}
