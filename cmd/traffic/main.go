package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	target := os.Getenv("TRAFFIC_TARGET_URL")
	if target == "" {
		logger.Error("TRAFFIC_TARGET_URL is required")
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	client := &http.Client{Timeout: 5 * time.Second}
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			logger.Error("invalid traffic target", "error", err)
			os.Exit(1)
		}
		response, err := client.Do(request)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			logger.Warn("demo request failed", "error", err)
		} else {
			_ = response.Body.Close()
			logger.Info("demo request completed", "status", response.StatusCode)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
