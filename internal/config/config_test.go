package config

import (
	"strings"
	"testing"
	"time"
)

func TestParseDefaults(t *testing.T) {
	cfg, err := Parse(func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HTTPAddr != ":8080" || cfg.ShutdownTimeout != 10*time.Second {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
}

func TestParseOverrides(t *testing.T) {
	values := map[string]string{
		"INCIDENTPILOT_HTTP_ADDR":        "127.0.0.1:9090",
		"INCIDENTPILOT_SHUTDOWN_TIMEOUT": "3s",
	}
	cfg, err := Parse(func(key string) string { return values[key] })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HTTPAddr != "127.0.0.1:9090" || cfg.ShutdownTimeout != 3*time.Second {
		t.Fatalf("unexpected overrides: %+v", cfg)
	}
}

func TestParseRejectsInvalidValues(t *testing.T) {
	tests := []struct {
		name   string
		key    string
		value  string
		wanted string
	}{
		{"missing port", "INCIDENTPILOT_HTTP_ADDR", "localhost", "INCIDENTPILOT_HTTP_ADDR"},
		{"zero port", "INCIDENTPILOT_HTTP_ADDR", ":0", "INCIDENTPILOT_HTTP_ADDR"},
		{"large port", "INCIDENTPILOT_HTTP_ADDR", ":65536", "INCIDENTPILOT_HTTP_ADDR"},
		{"bad duration", "INCIDENTPILOT_SHUTDOWN_TIMEOUT", "later", "INCIDENTPILOT_SHUTDOWN_TIMEOUT"},
		{"zero duration", "INCIDENTPILOT_SHUTDOWN_TIMEOUT", "0s", "INCIDENTPILOT_SHUTDOWN_TIMEOUT"},
		{"database without token", "INCIDENTPILOT_DATABASE_URL", "postgres://localhost/example", "must be set together"},
		{"token without database", "INCIDENTPILOT_WEBHOOK_TOKEN", "long-test-token-value", "must be set together"},
		{"MCP endpoint without token", "INCIDENTPILOT_MCP_ENDPOINT", "http://mcp.example/mcp", "must be set together"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse(func(key string) string {
				if key == tt.key {
					return tt.value
				}
				return ""
			})
			if err == nil || !strings.Contains(err.Error(), tt.wanted) {
				t.Fatalf("expected %s error, got %v", tt.wanted, err)
			}
		})
	}
}

func TestParseMCPConfiguration(t *testing.T) {
	values := map[string]string{"INCIDENTPILOT_DATABASE_URL": "postgres://localhost/example", "INCIDENTPILOT_WEBHOOK_TOKEN": "long-local-test-token", "INCIDENTPILOT_MCP_ENDPOINT": "http://mcp.example/mcp", "INCIDENTPILOT_MCP_TOKEN": "long-local-mcp-test-token"}
	if _, err := Parse(func(key string) string { return values[key] }); err != nil {
		t.Fatal(err)
	}
	values["INCIDENTPILOT_MCP_ENDPOINT"] = "https://mcp.example/not-mcp"
	if _, err := Parse(func(key string) string { return values[key] }); err == nil {
		t.Fatal("invalid MCP endpoint accepted")
	}
}

func TestParseRemediationConfiguration(t *testing.T) {
	values := map[string]string{
		"INCIDENTPILOT_DATABASE_URL":      "postgres://localhost/example",
		"INCIDENTPILOT_WEBHOOK_TOKEN":     "long-local-test-token",
		"INCIDENTPILOT_REMEDIATION_TOKEN": "long-local-remediation-token",
		"INCIDENTPILOT_ENVIRONMENT":       "development",
		"INCIDENTPILOT_REPOSITORY":        "owner/repository",
		"INCIDENTPILOT_REMEDIATION_PATH":  "deploy/kind/30-demo.yaml",
	}
	if _, err := Parse(func(key string) string { return values[key] }); err != nil {
		t.Fatal(err)
	}
	values["INCIDENTPILOT_REMEDIATION_PATH"] = "../secret"
	if _, err := Parse(func(key string) string { return values[key] }); err == nil {
		t.Fatal("unsafe remediation path accepted")
	}
	delete(values, "INCIDENTPILOT_REMEDIATION_PATH")
	if _, err := Parse(func(key string) string { return values[key] }); err == nil {
		t.Fatal("partial remediation configuration accepted")
	}
}
