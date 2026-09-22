package telemetry

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
)

func TestTraceContextRoundTrip(t *testing.T) {
	previous := otel.GetTextMapPropagator()
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() { otel.SetTextMapPropagator(previous) })

	original := Restore(context.Background(), TraceContext{
		TraceParent: "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
		TraceState:  "vendor=value",
	})
	if got := TraceID(original); got != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Fatalf("unexpected trace ID %q", got)
	}
	captured := Capture(original)
	if captured.TraceParent != "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01" || captured.TraceState != "vendor=value" {
		t.Fatalf("unexpected captured context: %+v", captured)
	}
}

func TestRestoreRejectsInvalidTraceParent(t *testing.T) {
	previous := otel.GetTextMapPropagator()
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() { otel.SetTextMapPropagator(previous) })
	if got := TraceID(Restore(context.Background(), TraceContext{TraceParent: "not-a-trace"})); got != "" {
		t.Fatalf("invalid trace context produced trace ID %q", got)
	}
}
