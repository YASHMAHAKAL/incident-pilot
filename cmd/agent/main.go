package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"incidentpilot/internal/agent"
	"incidentpilot/internal/evidence"
	"incidentpilot/internal/llm"
	"incidentpilot/internal/storage"
	"incidentpilot/internal/telemetry"
)

func main() {
	incidentID := flag.String("incident-id", "", "investigate an incident UUID")
	reportID := flag.String("report-id", "", "retrieve a persisted investigation UUID")
	flag.Parse()
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, *incidentID, *reportID, logger); err != nil {
		logger.Error("agent stopped", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, incidentID, reportID string, logger *slog.Logger) error {
	if (incidentID == "") == (reportID == "") {
		return errors.New("set exactly one of -incident-id or -report-id")
	}
	databaseURL := os.Getenv("INCIDENTPILOT_DATABASE_URL")
	if databaseURL == "" {
		return errors.New("INCIDENTPILOT_DATABASE_URL is required")
	}
	if os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") != "" {
		shutdown, err := telemetry.Start(ctx, "incidentpilot-agent")
		if err != nil {
			return fmt.Errorf("start telemetry: %w", err)
		}
		defer func() {
			flushCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := shutdown(flushCtx); err != nil {
				logger.Error("telemetry shutdown failed", "error", err)
			}
		}()
	}
	connectCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	postgres, err := storage.Open(connectCtx, databaseURL)
	cancel()
	if err != nil {
		return err
	}
	defer postgres.Close()
	var result any
	if reportID != "" {
		readCtx, readCancel := context.WithTimeout(ctx, 5*time.Second)
		defer readCancel()
		result, err = postgres.GetInvestigation(readCtx, reportID)
	} else {
		endpoint, token := os.Getenv("INCIDENTPILOT_MCP_ENDPOINT"), os.Getenv("INCIDENTPILOT_MCP_TOKEN")
		caller, callerErr := evidence.NewMCPClient(endpoint, token)
		if callerErr != nil {
			return fmt.Errorf("configure MCP: %w", callerErr)
		}
		cfg, cfgErr := llm.LoadConfig()
		if cfgErr != nil {
			return cfgErr
		}
		provider, providerErr := llm.NewProvider(cfg)
		if providerErr != nil {
			return providerErr
		}
		investigator := agent.Investigator{Incidents: postgres, Collector: evidence.Collector{Caller: caller, Store: postgres}, Provider: provider, Reports: postgres}
		result, err = investigator.Run(ctx, incidentID)
	}
	if err != nil {
		return err
	}
	if report, ok := result.(agent.Report); ok {
		logger.InfoContext(ctx, "investigation completed", "incident_id", report.IncidentID, "investigation_id", report.ID, "trace_id", report.TraceID, "status", report.Status, "llm_calls", report.LLMCalls, "tool_calls", report.ToolCalls)
	}
	return json.NewEncoder(os.Stdout).Encode(result)
}
