package telemetry

import (
	"context"
	"testing"

	oteltrace "go.opentelemetry.io/otel/trace"
)

func TestTraceParentFromContextReturnsCanonicalW3CValue(t *testing.T) {
	spanContext := oteltrace.NewSpanContext(oteltrace.SpanContextConfig{
		TraceID:    mustTraceID(t, "0102030405060708090a0b0c0d0e0f10"),
		SpanID:     mustSpanID(t, "0102030405060708"),
		TraceFlags: oteltrace.FlagsSampled,
		Remote:     true,
	})
	ctx := oteltrace.ContextWithRemoteSpanContext(context.Background(), spanContext)

	got, ok := TraceParentFromContext(ctx)
	if !ok || got != "00-0102030405060708090a0b0c0d0e0f10-0102030405060708-01" {
		t.Fatalf("traceparent = %q, %t", got, ok)
	}
}

func TestTraceParentFromContextOmitsMissingAndInvalidContexts(t *testing.T) {
	if value, ok := TraceParentFromContext(context.Background()); ok || value != "" {
		t.Fatalf("missing traceparent = %q, %t", value, ok)
	}
	invalid := oteltrace.NewSpanContext(oteltrace.SpanContextConfig{
		SpanID: mustSpanID(t, "0102030405060708"),
	})
	ctx := oteltrace.ContextWithSpanContext(context.Background(), invalid)
	if value, ok := TraceParentFromContext(ctx); ok || value != "" {
		t.Fatalf("invalid traceparent = %q, %t", value, ok)
	}
}

func TestIsValidTraceParentAcceptsOnlyCanonicalValidValues(t *testing.T) {
	valid := "00-0102030405060708090a0b0c0d0e0f10-0102030405060708-01"
	if !IsValidTraceParent(valid) {
		t.Fatalf("valid traceparent %q was rejected", valid)
	}
	for _, value := range []string{
		"",
		"invalid",
		"00-00000000000000000000000000000000-0102030405060708-01",
		"00-0102030405060708090a0b0c0d0e0f10-0000000000000000-01",
		"00-0102030405060708090A0B0C0D0E0F10-0102030405060708-01",
	} {
		if IsValidTraceParent(value) {
			t.Errorf("invalid traceparent %q was accepted", value)
		}
	}
}

func mustTraceID(t *testing.T, value string) oteltrace.TraceID {
	t.Helper()
	id, err := oteltrace.TraceIDFromHex(value)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func mustSpanID(t *testing.T, value string) oteltrace.SpanID {
	t.Helper()
	id, err := oteltrace.SpanIDFromHex(value)
	if err != nil {
		t.Fatal(err)
	}
	return id
}
