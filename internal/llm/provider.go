package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
)

const (
	groqEndpoint   = "https://api.groq.com/openai/v1/chat/completions"
	ollamaEndpoint = "http://127.0.0.1:11434/api/chat"
	maxBodyBytes   = 1 << 20
)

type Config struct {
	Provider            string
	Model               string
	APIKey              string
	Endpoint            string
	GroqReasoningEffort string
}

// LoadConfig leaves inference disabled when no provider is selected.
func LoadConfig() (Config, error) { return ParseConfig(os.Getenv) }

func ParseConfig(getenv func(string) string) (Config, error) {
	cfg := Config{
		Provider:            strings.ToLower(strings.TrimSpace(getenv("INCIDENTPILOT_LLM_PROVIDER"))),
		Model:               strings.TrimSpace(getenv("INCIDENTPILOT_LLM_MODEL")),
		APIKey:              getenv("INCIDENTPILOT_LLM_API_KEY"),
		Endpoint:            strings.TrimSpace(getenv("INCIDENTPILOT_LLM_ENDPOINT")),
		GroqReasoningEffort: strings.ToLower(strings.TrimSpace(getenv("INCIDENTPILOT_GROQ_REASONING_EFFORT"))),
	}
	if cfg.Provider == "" {
		if cfg.Model != "" || cfg.APIKey != "" || cfg.Endpoint != "" || cfg.GroqReasoningEffort != "" {
			return Config{}, errors.New("INCIDENTPILOT_LLM_PROVIDER is required when LLM settings are set")
		}
		return cfg, nil
	}
	if cfg.Model == "" || len(cfg.Model) > 128 {
		return Config{}, errors.New("INCIDENTPILOT_LLM_MODEL is required and must be at most 128 bytes")
	}
	switch cfg.Provider {
	case "groq":
		if cfg.APIKey == "" {
			return Config{}, errors.New("INCIDENTPILOT_LLM_API_KEY is required for Groq")
		}
		if cfg.Endpoint != "" && cfg.Endpoint != groqEndpoint {
			return Config{}, errors.New("Groq endpoint is fixed to the official HTTPS API")
		}
		cfg.Endpoint = groqEndpoint
		if cfg.Model == "openai/gpt-oss-20b" || cfg.Model == "openai/gpt-oss-120b" {
			if cfg.GroqReasoningEffort == "" {
				cfg.GroqReasoningEffort = "low"
			}
			if cfg.GroqReasoningEffort != "low" && cfg.GroqReasoningEffort != "medium" && cfg.GroqReasoningEffort != "high" {
				return Config{}, errors.New("Groq GPT-OSS reasoning effort must be low, medium, or high")
			}
		} else if cfg.GroqReasoningEffort != "" {
			return Config{}, errors.New("Groq reasoning effort is supported only for GPT-OSS models")
		}
	case "ollama":
		if cfg.GroqReasoningEffort != "" {
			return Config{}, errors.New("Groq reasoning effort is not used by Ollama")
		}
		if cfg.APIKey != "" {
			return Config{}, errors.New("INCIDENTPILOT_LLM_API_KEY is not used by Ollama")
		}
		if cfg.Endpoint == "" {
			cfg.Endpoint = ollamaEndpoint
		}
		u, err := url.Parse(cfg.Endpoint)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.Path != "/api/chat" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return Config{}, errors.New("INCIDENTPILOT_LLM_ENDPOINT must be an http(s) /api/chat URL for Ollama")
		}
	default:
		return Config{}, errors.New("INCIDENTPILOT_LLM_PROVIDER must be groq or ollama")
	}
	return cfg, nil
}

// NewProvider selects a wire adapter without exposing it to callers.
func NewProvider(cfg Config) (Provider, error) {
	if cfg.Provider == "" {
		return nil, errors.New("LLM provider is disabled")
	}
	values := map[string]string{
		"INCIDENTPILOT_LLM_PROVIDER":          cfg.Provider,
		"INCIDENTPILOT_LLM_MODEL":             cfg.Model,
		"INCIDENTPILOT_LLM_API_KEY":           cfg.APIKey,
		"INCIDENTPILOT_LLM_ENDPOINT":          cfg.Endpoint,
		"INCIDENTPILOT_GROQ_REASONING_EFFORT": cfg.GroqReasoningEffort,
	}
	checked, err := ParseConfig(func(k string) string { return values[k] })
	if err != nil {
		return nil, err
	}
	return &httpProvider{config: checked, client: &http.Client{
		Timeout:       60 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}, nil
}

type httpProvider struct {
	config Config
	client *http.Client
}

func (p *httpProvider) Chat(ctx context.Context, req ChatRequest) (ChatResponse, error) {
	started := time.Now()
	status := "error"
	ctx, span := otel.Tracer("incidentpilot/llm").Start(ctx, "llm.chat")
	span.SetAttributes(attribute.String("llm.provider", p.config.Provider), attribute.String("llm.model", p.config.Model))
	meter := otel.Meter("incidentpilot/llm")
	requests, _ := meter.Int64Counter("incidentpilot_llm_requests_total")
	durations, _ := meter.Float64Histogram("incidentpilot_llm_request_duration_seconds", metric.WithUnit("s"))
	labels := metric.WithAttributes(attribute.String("provider", p.config.Provider), attribute.String("model", p.config.Model))
	requests.Add(ctx, 1, labels)
	defer func() {
		durations.Record(ctx, time.Since(started).Seconds(), metric.WithAttributes(attribute.String("provider", p.config.Provider), attribute.String("model", p.config.Model), attribute.String("status", status)))
		span.End()
	}()
	if req.Model == "" {
		req.Model = p.config.Model
	}
	if req.Model != p.config.Model {
		return ChatResponse{}, fmt.Errorf("%w: model must match configured model", ErrInvalidRequest)
	}
	if err := validateRequest(req); err != nil {
		return ChatResponse{}, err
	}
	var payload any
	if p.config.Provider == "groq" {
		if len(req.JSONSchema) > 0 && !groqStrictSchemaSupported(p.config.Model) {
			return ChatResponse{}, ErrUnsupportedCapability
		}
		payload = groqRequest(req, p.config)
	} else {
		payload = ollamaRequest(req)
	}
	data, err := json.Marshal(payload)
	if err != nil || len(data) > maxBodyBytes {
		return ChatResponse{}, fmt.Errorf("%w: request exceeds 1 MiB", ErrInvalidRequest)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.config.Endpoint, bytes.NewReader(data))
	if err != nil {
		return ChatResponse{}, fmt.Errorf("prepare LLM request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if p.config.Provider == "groq" {
		httpReq.Header.Set("Authorization", "Bearer "+p.config.APIKey)
	}
	httpRes, err := p.client.Do(httpReq)
	if err != nil {
		span.SetStatus(codes.Error, "transport error")
		if ctx.Err() != nil {
			return ChatResponse{}, ctx.Err()
		}
		return ChatResponse{}, ErrTransport
	}
	defer httpRes.Body.Close()
	if httpRes.StatusCode != http.StatusOK {
		span.SetStatus(codes.Error, "provider error")
		span.SetAttributes(attribute.Int("http.response.status_code", httpRes.StatusCode))
		httpErr := ProviderHTTPError{StatusCode: httpRes.StatusCode}
		if p.config.Provider == "groq" {
			if httpRes.StatusCode == http.StatusTooManyRequests {
				httpErr.RetryAfter = retryAfterDelay(httpRes.Header.Get("Retry-After"), time.Now())
			}
			if httpRes.StatusCode == http.StatusBadRequest {
				body, readErr := io.ReadAll(io.LimitReader(httpRes.Body, 4096))
				if readErr == nil {
					httpErr.detail = classifyGroqBadRequest(body)
				}
			}
		}
		return ChatResponse{}, httpErr
	}
	body, err := io.ReadAll(io.LimitReader(httpRes.Body, maxBodyBytes+1))
	if err != nil || len(body) > maxBodyBytes {
		return ChatResponse{}, invalidResponseError{"unreadable_or_oversized_output"}
	}
	var result ChatResponse
	if p.config.Provider == "groq" {
		result, err = parseGroq(body)
	} else {
		result, err = parseOllama(body)
	}
	if err != nil {
		span.SetStatus(codes.Error, "invalid response")
		return ChatResponse{}, err
	}
	if err := validateResponse(req, result); err != nil {
		span.SetStatus(codes.Error, "invalid response")
		return ChatResponse{}, err
	}
	span.SetAttributes(attribute.Int("llm.input_tokens", result.Usage.InputTokens), attribute.Int("llm.output_tokens", result.Usage.OutputTokens))
	tokens, _ := meter.Int64Counter("incidentpilot_llm_tokens_total")
	tokens.Add(ctx, int64(result.Usage.InputTokens), metric.WithAttributes(attribute.String("provider", p.config.Provider), attribute.String("model", p.config.Model), attribute.String("direction", "input")))
	tokens.Add(ctx, int64(result.Usage.OutputTokens), metric.WithAttributes(attribute.String("provider", p.config.Provider), attribute.String("model", p.config.Model), attribute.String("direction", "output")))
	status = "ok"
	return result, nil
}

func retryAfterDelay(raw string, now time.Time) time.Duration {
	if seconds, err := strconv.Atoi(strings.TrimSpace(raw)); err == nil && seconds > 0 && seconds <= 300 {
		return time.Duration(seconds) * time.Second
	}
	if deadline, err := http.ParseTime(raw); err == nil && deadline.After(now) {
		return deadline.Sub(now)
	}
	return 0
}

// Only fixed categories leave this adapter. Never retain or log the provider
// message or failed_generation payload; either may echo untrusted content.
func classifyGroqBadRequest(body []byte) string {
	var response struct {
		Error struct {
			Code             string          `json:"code"`
			Type             string          `json:"type"`
			FailedGeneration json.RawMessage `json:"failed_generation"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &response) != nil {
		return ""
	}
	if len(response.Error.FailedGeneration) > 0 && string(response.Error.FailedGeneration) != "null" {
		return "tool_generation_failed"
	}
	switch response.Error.Code {
	case "tool_use_failed", "json_validate_failed":
		return response.Error.Code
	}
	if response.Error.Type == "invalid_request_error" {
		return "invalid_request"
	}
	return ""
}
