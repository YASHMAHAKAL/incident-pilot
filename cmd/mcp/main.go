package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"incidentpilot/internal/investigation"
	"incidentpilot/internal/telemetry"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	shutdown, err := telemetry.Start(ctx, "incidentpilot-mcp")
	if err != nil {
		logger.Error("start telemetry", "error", err)
		os.Exit(1)
	}
	defer func() {
		flushCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = shutdown(flushCtx)
	}()
	ca, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/ca.crt")
	if err != nil {
		logger.Error("read Kubernetes CA", "error", err)
		return
	}
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if !pool.AppendCertsFromPEM(ca) {
		logger.Error("invalid Kubernetes CA")
		return
	}
	kubeToken, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/token")
	if err != nil {
		logger.Error("read Kubernetes token", "error", err)
		return
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	client := &http.Client{Transport: otelhttp.NewTransport(transport), Timeout: 9 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirects disabled") }}
	githubAPI := os.Getenv("INCIDENTPILOT_GITHUB_API_URL")
	if githubAPI == "" && os.Getenv("INCIDENTPILOT_GITHUB_REPOSITORY") != "" {
		githubAPI = "https://api.github.com"
	}
	backend := investigation.Backend{
		Client: client, Kubernetes: "https://kubernetes.default.svc", Prometheus: "http://prometheus.incidentpilot-observability.svc.cluster.local:9090", Loki: "http://loki.incidentpilot-observability.svc.cluster.local:3100", Tempo: "http://tempo.incidentpilot-observability.svc.cluster.local:3200", Token: string(kubeToken),
		ArgoCD: os.Getenv("INCIDENTPILOT_ARGOCD_URL"), ArgoCDToken: os.Getenv("INCIDENTPILOT_ARGOCD_TOKEN"), ArgoCDApplication: os.Getenv("INCIDENTPILOT_ARGOCD_APPLICATION"), ArgoCDProject: os.Getenv("INCIDENTPILOT_ARGOCD_PROJECT"),
		GitHub: githubAPI, GitHubToken: os.Getenv("INCIDENTPILOT_GITHUB_TOKEN"), GitHubRepository: os.Getenv("INCIDENTPILOT_GITHUB_REPOSITORY"),
	}
	if err := backend.ValidateChangeSources(); err != nil {
		logger.Error("change source config", "error", err)
		return
	}
	remediationEndpoint, remediationToken := os.Getenv("INCIDENTPILOT_REMEDIATION_ENDPOINT"), os.Getenv("INCIDENTPILOT_REMEDIATION_TOKEN")
	if (remediationEndpoint == "") != (remediationToken == "") {
		logger.Error("remediation config", "error", "endpoint and token must be set together")
		return
	}
	var remediationClient investigation.RemediationRequester
	if remediationEndpoint != "" {
		remediationHTTP := &http.Client{Transport: client.Transport, Timeout: 50 * time.Second, CheckRedirect: client.CheckRedirect}
		remediationClient, err = investigation.NewRemediationClient(remediationHTTP, remediationEndpoint, remediationToken)
		if err != nil {
			logger.Error("remediation config", "error", err)
			return
		}
	}
	server, err := investigation.NewServerWithRemediation(backend, remediationClient, os.Getenv("INCIDENTPILOT_MCP_TOKEN"), logger)
	if err != nil {
		logger.Error("MCP config", "error", err)
		return
	}
	addr := ":8081"
	if value := os.Getenv("INCIDENTPILOT_MCP_LISTEN"); value != "" {
		addr = value
	}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		logger.Error("listen", "error", err)
		return
	}
	httpServer := &http.Server{Handler: otelhttp.NewHandler(server.Handler(), "incidentpilot.mcp.http"), ReadHeaderTimeout: 5 * time.Second, MaxHeaderBytes: 8 << 10}
	go func() {
		<-ctx.Done()
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(closeCtx)
	}()
	logger.Info("MCP listening", "address", addr)
	if err := httpServer.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("MCP server", "error", err)
	}
}
