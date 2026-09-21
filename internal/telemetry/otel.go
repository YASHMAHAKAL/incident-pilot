package telemetry

import (
	"context"
	"errors"
	"runtime"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	metricapi "go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	"go.opentelemetry.io/otel/sdk/trace"
)

// Start exports traces and metrics using standard OTLP environment variables.
// The caller must shut the providers down to flush buffered telemetry.
func Start(ctx context.Context, serviceName string) (func(context.Context) error, error) {
	res := resource.NewWithAttributes("", attribute.String("service.name", serviceName))
	traceExporter, err := otlptracehttp.New(ctx)
	if err != nil {
		return nil, err
	}
	metricExporter, err := otlpmetrichttp.New(ctx)
	if err != nil {
		_ = traceExporter.Shutdown(ctx)
		return nil, err
	}
	traceProvider := trace.NewTracerProvider(trace.WithBatcher(traceExporter), trace.WithResource(res))
	metricProvider := metric.NewMeterProvider(
		metric.WithResource(res),
		metric.WithReader(metric.NewPeriodicReader(metricExporter, metric.WithInterval(5*time.Second))),
	)
	meter := metricProvider.Meter("incidentpilot/runtime")
	heapGauge, err := meter.Int64ObservableGauge("demo_process_heap_alloc_bytes", metricapi.WithUnit("By"))
	if err != nil {
		_ = metricProvider.Shutdown(ctx)
		_ = traceProvider.Shutdown(ctx)
		return nil, err
	}
	_, err = meter.RegisterCallback(func(_ context.Context, observer metricapi.Observer) error {
		var stats runtime.MemStats
		runtime.ReadMemStats(&stats)
		observer.ObserveInt64(heapGauge, int64(stats.HeapAlloc))
		return nil
	}, heapGauge)
	if err != nil {
		_ = metricProvider.Shutdown(ctx)
		_ = traceProvider.Shutdown(ctx)
		return nil, err
	}
	otel.SetTracerProvider(traceProvider)
	otel.SetMeterProvider(metricProvider)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))
	return func(shutdownCtx context.Context) error {
		return errors.Join(metricProvider.Shutdown(shutdownCtx), traceProvider.Shutdown(shutdownCtx))
	}, nil
}
