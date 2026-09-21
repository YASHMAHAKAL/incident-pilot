package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"incidentpilot/internal/demo"
	"incidentpilot/internal/telemetry"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, logger); err != nil {
		logger.Error("demo stopped", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, logger *slog.Logger) error {
	role := os.Getenv("DEMO_ROLE")
	workingSetMiB := 0
	if value := os.Getenv("DEMO_PAYMENT_WORKING_SET_MIB"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil {
			return errors.New("DEMO_PAYMENT_WORKING_SET_MIB must be an integer")
		}
		workingSetMiB = parsed
	}
	orderCPUBurnMs := 0
	if value := os.Getenv("DEMO_ORDER_CPU_BURN_MS"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil {
			return errors.New("DEMO_ORDER_CPU_BURN_MS must be an integer")
		}
		orderCPUBurnMs = parsed
	}
	handler, err := demo.Handler(demo.Config{
		Role: role, Upstream: os.Getenv("DEMO_UPSTREAM_URL"),
		OrderMode: os.Getenv("DEMO_ORDER_MODE"), OrderCPUBurn: time.Duration(orderCPUBurnMs) * time.Millisecond,
		PaymentMode: os.Getenv("DEMO_PAYMENT_MODE"), PaymentWorkingSetMiB: workingSetMiB,
	}, logger, nil)
	if err != nil {
		return err
	}
	shutdownTelemetry, err := telemetry.Start(ctx, role)
	if err != nil {
		return err
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := shutdownTelemetry(shutdownCtx); err != nil {
			logger.Error("telemetry shutdown failed", "error", err)
		}
	}()
	address := os.Getenv("DEMO_HTTP_ADDR")
	if address == "" {
		address = ":8080"
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return err
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	shutdownDone := make(chan error, 1)
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			_ = server.Close()
			shutdownDone <- err
			return
		}
		shutdownDone <- nil
	}()
	logger.Info("demo listening", "service", role, "address", listener.Addr().String())
	if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return <-shutdownDone
}
