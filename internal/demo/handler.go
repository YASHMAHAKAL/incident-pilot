package demo

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"runtime"
	"strings"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

const RequestTimeout = 3 * time.Second

type Config struct {
	Role                 string
	Upstream             string
	OrderMode            string
	OrderCPUBurn         time.Duration
	PaymentMode          string
	PaymentWorkingSetMiB int
}

func Handler(config Config, logger *slog.Logger, client *http.Client) (http.Handler, error) {
	role, upstream := config.Role, config.Upstream
	if role != "frontend" && role != "orders-api" && role != "payments-api" {
		return nil, fmt.Errorf("unknown demo role %q", role)
	}
	if role != "payments-api" && upstream == "" {
		return nil, fmt.Errorf("upstream URL required for %s", role)
	}
	if config.PaymentWorkingSetMiB < 0 || config.PaymentWorkingSetMiB > 96 || (role != "payments-api" && config.PaymentWorkingSetMiB != 0) {
		return nil, fmt.Errorf("invalid payment working set: %d MiB", config.PaymentWorkingSetMiB)
	}
	if config.OrderMode == "" {
		config.OrderMode = "normal"
	}
	if config.OrderMode != "normal" {
		return nil, fmt.Errorf("invalid order mode %q", config.OrderMode)
	}
	if config.OrderCPUBurn < 0 || config.OrderCPUBurn > 1500*time.Millisecond || (role != "orders-api" && config.OrderCPUBurn != 0) {
		return nil, fmt.Errorf("invalid order CPU burn duration %s", config.OrderCPUBurn)
	}
	if config.PaymentMode == "" {
		config.PaymentMode = "normal"
	}
	if (config.PaymentMode != "normal" && config.PaymentMode != "fail") || (role != "payments-api" && config.PaymentMode != "normal") {
		return nil, fmt.Errorf("invalid payment mode %q", config.PaymentMode)
	}
	workingSet := &paymentMemory{targetBytes: config.PaymentWorkingSetMiB << 20}
	if client == nil {
		client = &http.Client{Transport: otelhttp.NewTransport(http.DefaultTransport), Timeout: RequestTimeout}
	}
	counter, err := otel.Meter("incidentpilot/demo").Int64Counter("demo_requests")
	if err != nil {
		return nil, err
	}
	duration, err := otel.Meter("incidentpilot/demo").Float64Histogram("demo_request_duration_seconds", metric.WithUnit("s"))
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	if role == "frontend" {
		mux.HandleFunc("GET /", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte("<h1>IncidentPilot demo shop</h1><a href=\"/checkout\">Place order</a>\n"))
		})
	}
	route := map[string]string{"frontend": "/checkout", "orders-api": "/order", "payments-api": "/charge"}[role]
	mux.HandleFunc("GET "+route, func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		started := time.Now()
		status := "success"
		defer func() {
			labels := metric.WithAttributes(attribute.String("role", role), attribute.String("outcome", status))
			counter.Add(ctx, 1, labels)
			duration.Record(ctx, time.Since(started).Seconds(), labels)
			span := trace.SpanFromContext(ctx).SpanContext()
			logger.InfoContext(ctx, "demo request", "service", role, "outcome", status, "duration_ms", time.Since(started).Milliseconds(), "trace_id", span.TraceID().String())
		}()
		if role == "orders-api" && config.OrderCPUBurn > 0 {
			if err := burnCPU(ctx, config.OrderCPUBurn); err != nil {
				status = "work_interrupted"
				trace.SpanFromContext(ctx).RecordError(err)
				http.Error(w, "order work interrupted", http.StatusServiceUnavailable)
				return
			}
		}
		if role != "payments-api" {
			next := map[string]string{"frontend": "/order", "orders-api": "/charge"}[role]
			if err := callUpstream(ctx, client, strings.TrimRight(upstream, "/")+next); err != nil {
				status = "upstream_error"
				trace.SpanFromContext(ctx).RecordError(err)
				trace.SpanFromContext(ctx).SetStatus(codes.Error, err.Error())
				http.Error(w, "upstream unavailable", http.StatusBadGateway)
				return
			}
		} else {
			if config.PaymentMode == "fail" {
				status = "downstream_failure"
				err := errors.New("simulated payment processor failure")
				trace.SpanFromContext(ctx).RecordError(err)
				trace.SpanFromContext(ctx).SetStatus(codes.Error, err.Error())
				http.Error(w, "payment processor unavailable", http.StatusServiceUnavailable)
				return
			}
			if bytes, grew := workingSet.grow(); grew {
				logger.InfoContext(ctx, "payment working set grew", "working_set_bytes", bytes)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"service": role, "status": "ok"})
	})
	return otelhttp.NewHandler(mux, role+".http"), nil
}

func burnCPU(ctx context.Context, work time.Duration) error {
	deadline := time.Now().Add(work)
	var digest [32]byte
	for time.Now().Before(deadline) {
		digest = sha256.Sum256(digest[:])
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	runtime.KeepAlive(digest)
	return nil
}

func callUpstream(ctx context.Context, client *http.Client, target string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return err
	}
	response, err := client.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("upstream returned HTTP %d", response.StatusCode)
	}
	return nil
}
