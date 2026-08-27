package telemetry

import (
	"context"
	"log/slog"

	"go.opentelemetry.io/otel/trace"
)

type traceHandler struct {
	next slog.Handler
}

func NewTraceHandler(next slog.Handler) slog.Handler {
	return traceHandler{next: next}
}

func (handler traceHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return handler.next.Enabled(ctx, level)
}

func (handler traceHandler) Handle(ctx context.Context, record slog.Record) error {
	spanContext := trace.SpanContextFromContext(ctx)
	if spanContext.IsValid() {
		record.AddAttrs(
			slog.String("trace_id", spanContext.TraceID().String()),
			slog.String("span_id", spanContext.SpanID().String()),
		)
	}
	return handler.next.Handle(ctx, record)
}

func (handler traceHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return traceHandler{next: handler.next.WithAttrs(attrs)}
}

func (handler traceHandler) WithGroup(name string) slog.Handler {
	return traceHandler{next: handler.next.WithGroup(name)}
}
