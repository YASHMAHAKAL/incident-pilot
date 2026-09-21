package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"incidentpilot/internal/config"
	"incidentpilot/internal/evidence"
	"incidentpilot/internal/httpapi"
	"incidentpilot/internal/incident"
	"incidentpilot/internal/storage"
	"incidentpilot/internal/telemetry"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg, err := config.Load()
	if err != nil {
		logger.Error("invalid configuration", "error", err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, cfg, logger); err != nil {
		logger.Error("api stopped", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	var store incident.Store
	var evidenceStore evidence.Store
	if cfg.DatabaseURL != "" {
		connectCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
		postgres, err := storage.Open(connectCtx, cfg.DatabaseURL)
		cancel()
		if err != nil {
			return err
		}
		defer postgres.Close()
		store = postgres
		evidenceStore = postgres
	}
	if os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") != "" {
		shutdownTelemetry, err := telemetry.Start(ctx, "incidentpilot-api")
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
	}
	listener, err := net.Listen("tcp", cfg.HTTPAddr)
	if err != nil {
		return err
	}
	var collector *evidence.Collector
	if cfg.MCPEndpoint != "" {
		caller, err := evidence.NewMCPClient(cfg.MCPEndpoint, cfg.MCPToken)
		if err != nil {
			return err
		}
		collector = &evidence.Collector{Caller: caller, Store: evidenceStore}
	}
	server := &http.Server{
		Handler:           httpapi.HandlerWithEvidence(store, evidenceStore, collector, cfg.WebhookToken, logger),
		ReadHeaderTimeout: 5 * time.Second,
	}
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			logger.Error("graceful shutdown failed", "error", err)
			_ = server.Close()
		}
	}()
	logger.Info("api listening", "address", listener.Addr().String())
	err = server.Serve(listener)
	if errors.Is(err, http.ErrServerClosed) {
		<-shutdownDone
		return nil
	}
	return err
}
