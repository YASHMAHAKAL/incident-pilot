package config

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"incidentpilot/internal/onboarding"
)

const (
	defaultHTTPAddr        = ":8080"
	defaultShutdownTimeout = 10 * time.Second
)

type Config struct {
	Onboarding        onboarding.Profile
	HTTPAddr          string
	ShutdownTimeout   time.Duration
	DatabaseURL       string
	WebhookToken      string
	MCPEndpoint       string
	MCPToken          string
	RemediationToken  string
	Environment       string
	Repository        string
	RemediationPath   string
	GitHubWriteAPIURL string
	GitHubWriteToken  string
	GitHubBaseBranch  string
}

func Load() (Config, error) {
	return Parse(os.Getenv)
}

// Parse accepts an environment lookup so configuration can be tested without
// changing the process environment.
func Parse(getenv func(string) string) (Config, error) {
	cfg := Config{
		HTTPAddr:        defaultHTTPAddr,
		ShutdownTimeout: defaultShutdownTimeout,
	}
	profile, err := onboarding.Parse(getenv("INCIDENTPILOT_ONBOARDING_PROFILE"))
	if err != nil {
		return Config{}, err
	}
	cfg.Onboarding = profile
	if value := getenv("INCIDENTPILOT_HTTP_ADDR"); value != "" {
		cfg.HTTPAddr = value
	}
	if value := getenv("INCIDENTPILOT_SHUTDOWN_TIMEOUT"); value != "" {
		parsed, err := time.ParseDuration(value)
		if err != nil || parsed <= 0 {
			return Config{}, fmt.Errorf("INCIDENTPILOT_SHUTDOWN_TIMEOUT must be a positive duration")
		}
		cfg.ShutdownTimeout = parsed
	}
	cfg.DatabaseURL = getenv("INCIDENTPILOT_DATABASE_URL")
	cfg.WebhookToken = getenv("INCIDENTPILOT_WEBHOOK_TOKEN")
	cfg.MCPEndpoint = getenv("INCIDENTPILOT_MCP_ENDPOINT")
	cfg.MCPToken = getenv("INCIDENTPILOT_MCP_TOKEN")
	cfg.RemediationToken = getenv("INCIDENTPILOT_REMEDIATION_TOKEN")
	cfg.Environment = getenv("INCIDENTPILOT_ENVIRONMENT")
	cfg.Repository = getenv("INCIDENTPILOT_REPOSITORY")
	cfg.RemediationPath = getenv("INCIDENTPILOT_REMEDIATION_PATH")
	cfg.GitHubWriteAPIURL = getenv("INCIDENTPILOT_GITHUB_WRITE_API_URL")
	cfg.GitHubWriteToken = getenv("INCIDENTPILOT_GITHUB_WRITE_TOKEN")
	cfg.GitHubBaseBranch = getenv("INCIDENTPILOT_GITHUB_BASE_BRANCH")
	if (cfg.DatabaseURL == "") != (cfg.WebhookToken == "") {
		return Config{}, fmt.Errorf("INCIDENTPILOT_DATABASE_URL and INCIDENTPILOT_WEBHOOK_TOKEN must be set together")
	}
	if cfg.WebhookToken != "" && len(cfg.WebhookToken) < 16 {
		return Config{}, fmt.Errorf("INCIDENTPILOT_WEBHOOK_TOKEN must be at least 16 bytes")
	}
	if (cfg.MCPEndpoint == "") != (cfg.MCPToken == "") {
		return Config{}, fmt.Errorf("INCIDENTPILOT_MCP_ENDPOINT and INCIDENTPILOT_MCP_TOKEN must be set together")
	}
	if cfg.MCPEndpoint != "" {
		if cfg.DatabaseURL == "" {
			return Config{}, fmt.Errorf("MCP evidence collection requires PostgreSQL")
		}
		u, err := url.Parse(cfg.MCPEndpoint)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.Path != "/mcp" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return Config{}, fmt.Errorf("INCIDENTPILOT_MCP_ENDPOINT must be an http(s) /mcp URL")
		}
		if len(cfg.MCPToken) < 24 {
			return Config{}, fmt.Errorf("INCIDENTPILOT_MCP_TOKEN must be at least 24 bytes")
		}
	}
	remediationValues := []string{cfg.RemediationToken, cfg.Environment, cfg.Repository, cfg.RemediationPath}
	remediationConfigured := false
	for _, value := range remediationValues {
		remediationConfigured = remediationConfigured || value != ""
	}
	if remediationConfigured {
		if len(cfg.RemediationToken) < 24 || cfg.DatabaseURL == "" || cfg.Environment == "" || !repositoryName(cfg.Repository) || !repositoryPath(cfg.RemediationPath) {
			return Config{}, fmt.Errorf("remediation requires PostgreSQL, a 24-byte token, environment, owner/repository, and safe repository path")
		}
	}
	githubValues := []string{cfg.GitHubWriteAPIURL, cfg.GitHubWriteToken, cfg.GitHubBaseBranch}
	githubConfigured := false
	for _, value := range githubValues {
		githubConfigured = githubConfigured || value != ""
	}
	if githubConfigured {
		parsed, err := url.Parse(cfg.GitHubWriteAPIURL)
		if !remediationConfigured || err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || len(cfg.GitHubWriteToken) < 20 || !branchName(cfg.GitHubBaseBranch) {
			return Config{}, fmt.Errorf("GitHub remediation requires the remediation pipeline, a fixed API URL, 20-byte write token, and safe base branch")
		}
		if parsed.Scheme == "http" && !localHTTPHost(parsed.Hostname()) {
			return Config{}, fmt.Errorf("INCIDENTPILOT_GITHUB_WRITE_API_URL must use HTTPS outside loopback or the cluster")
		}
	}

	_, port, err := net.SplitHostPort(cfg.HTTPAddr)
	if err != nil {
		return Config{}, fmt.Errorf("INCIDENTPILOT_HTTP_ADDR must be host:port: %w", err)
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return Config{}, fmt.Errorf("INCIDENTPILOT_HTTP_ADDR must use a port from 1 to 65535")
	}
	return cfg, nil
}

func repositoryName(value string) bool {
	parts := strings.Split(value, "/")
	return len(parts) == 2 && dnsRepositoryPart(parts[0]) && dnsRepositoryPart(parts[1])
}

func dnsRepositoryPart(value string) bool {
	if value == "" || len(value) > 100 {
		return false
	}
	for _, char := range value {
		if !(char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '-' || char == '_' || char == '.') {
			return false
		}
	}
	return true
}

func repositoryPath(value string) bool {
	if value == "" || len(value) > 256 || strings.Contains(value, "..") || strings.HasPrefix(value, "/") {
		return false
	}
	for _, char := range value {
		if !(char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || strings.ContainsRune("_./-", char)) {
			return false
		}
	}
	return true
}

func branchName(value string) bool {
	if value == "" || len(value) > 128 || strings.Contains(value, "..") {
		return false
	}
	for _, char := range value {
		if !(char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || strings.ContainsRune("._-", char)) {
			return false
		}
	}
	return true
}

func localHTTPHost(host string) bool {
	host = strings.ToLower(host)
	return host == "localhost" || host == "127.0.0.1" || host == "::1" || strings.HasSuffix(host, ".svc") || strings.HasSuffix(host, ".svc.cluster.local")
}
