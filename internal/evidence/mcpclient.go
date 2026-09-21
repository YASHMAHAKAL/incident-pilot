package evidence

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

type MCPClient struct {
	endpoint string
	client   *http.Client
}

type bearerTransport struct {
	base  http.RoundTripper
	token string
}

func (t bearerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	copy := req.Clone(req.Context())
	copy.Header.Set("Authorization", "Bearer "+t.token)
	return t.base.RoundTrip(copy)
}

func NewMCPClient(endpoint, token string) (*MCPClient, error) {
	u, err := url.Parse(endpoint)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.Path != "/mcp" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("MCP endpoint must be an http(s) /mcp URL")
	}
	if len(token) < 24 {
		return nil, errors.New("MCP token must be at least 24 bytes")
	}
	base := otelhttp.NewTransport(http.DefaultTransport)
	client := &http.Client{Transport: bearerTransport{base: base, token: token}, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("MCP redirect refused") }}
	return &MCPClient{endpoint: endpoint, client: client}, nil
}

func (c *MCPClient) Call(ctx context.Context, tool string, args any) (ToolObservation, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	client := mcp.NewClient(&mcp.Implementation{Name: "incidentpilot-evidence", Version: "0.1.0"}, nil)
	transport := &mcp.StreamableClientTransport{Endpoint: c.endpoint, HTTPClient: c.client, DisableStandaloneSSE: true, MaxRetries: -1, MaxEventSize: 1 << 20}
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		return ToolObservation{}, fmt.Errorf("connect MCP: %w", err)
	}
	defer session.Close()
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		return ToolObservation{}, fmt.Errorf("call MCP tool %s: %w", tool, err)
	}
	if result.IsError {
		return ToolObservation{}, fmt.Errorf("MCP tool %s returned an error", tool)
	}
	if result.StructuredContent == nil {
		return ToolObservation{}, errors.New("MCP tool returned no structured content")
	}
	raw, err := json.Marshal(result.StructuredContent)
	if err != nil || len(raw) > 512<<10 {
		return ToolObservation{}, errors.New("MCP structured result invalid or oversized")
	}
	var observation ToolObservation
	if err := json.Unmarshal(raw, &observation); err != nil {
		return ToolObservation{}, fmt.Errorf("decode MCP result: %w", err)
	}
	return observation, nil
}
