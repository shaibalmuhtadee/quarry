package telemetry

import (
	"context"

	"go.opentelemetry.io/otel/propagation"
)

func TraceParentFromContext(ctx context.Context) (string, bool) {
	carrier := propagation.MapCarrier{}
	propagation.TraceContext{}.Inject(ctx, carrier)
	value := carrier.Get("traceparent")
	return value, value != ""
}

func IsValidTraceParent(value string) bool {
	if value == "" {
		return false
	}
	extracted := propagation.TraceContext{}.Extract(
		context.Background(),
		propagation.MapCarrier{"traceparent": value},
	)
	normalized, ok := TraceParentFromContext(extracted)
	return ok && normalized == value
}
