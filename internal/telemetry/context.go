package telemetry

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

// TraceContext is the minimal W3C propagation state persisted across
// asynchronous IncidentPilot boundaries. It never contains baggage.
type TraceContext struct {
	TraceParent string
	TraceState  string
}

func Capture(ctx context.Context) TraceContext {
	carrier := propagation.MapCarrier{}
	otel.GetTextMapPropagator().Inject(ctx, carrier)
	return TraceContext{TraceParent: carrier.Get("traceparent"), TraceState: carrier.Get("tracestate")}
}

func Restore(ctx context.Context, saved TraceContext) context.Context {
	if saved.TraceParent == "" {
		return ctx
	}
	carrier := propagation.MapCarrier{"traceparent": saved.TraceParent}
	if saved.TraceState != "" {
		carrier.Set("tracestate", saved.TraceState)
	}
	return otel.GetTextMapPropagator().Extract(ctx, carrier)
}

func TraceID(ctx context.Context) string {
	spanContext := trace.SpanContextFromContext(ctx)
	if !spanContext.IsValid() {
		return ""
	}
	return spanContext.TraceID().String()
}
