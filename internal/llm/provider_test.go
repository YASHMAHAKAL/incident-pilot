package llm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func sampleRequest() ChatRequest {
	return ChatRequest{
		Model:      "test-model",
		Messages:   []Message{{Role: RoleUser, Content: "inspect checkout"}},
		Tools:      []ToolDefinition{{Name: "get_pods", Description: "Read pods", Parameters: json.RawMessage(`{"type":"object","properties":{}}`)}},
		ToolChoice: ToolRequired,
		MaxTokens:  256,
	}
}

func TestConfigSwitchesProviders(t *testing.T) {
	tests := []struct {
		provider string
		key      string
		endpoint string
		wantErr  bool
	}{
		{"groq", "test-secret", "", false},
		{"ollama", "", "http://127.0.0.1:11434/api/chat", false},
		{"groq", "", "", true},
		{"ollama", "test-secret", "", true},
		{"invalid", "", "", true},
		{"ollama", "", "http://user:pass@localhost:11434/api/chat", true},
		{"groq", "test-secret", "https://other.example/chat", true},
	}
	for _, tc := range tests {
		t.Run(tc.provider+tc.key+tc.endpoint, func(t *testing.T) {
			values := map[string]string{"INCIDENTPILOT_LLM_PROVIDER": tc.provider, "INCIDENTPILOT_LLM_MODEL": "test-model", "INCIDENTPILOT_LLM_API_KEY": tc.key, "INCIDENTPILOT_LLM_ENDPOINT": tc.endpoint}
			cfg, err := ParseConfig(func(k string) string { return values[k] })
			if (err != nil) != tc.wantErr {
				t.Fatalf("unexpected config result: %v", err)
			}
			if !tc.wantErr {
				if _, err := NewProvider(cfg); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestAdaptersTranslateToolCalls(t *testing.T) {
	for _, provider := range []string{"groq", "ollama"} {
		t.Run(provider, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" {
					t.Errorf("unexpected request")
				}
				if provider == "groq" && r.Header.Get("Authorization") != "Bearer test-secret" {
					t.Errorf("missing Groq authorization")
				}
				if provider == "ollama" && r.Header.Get("Authorization") != "" {
					t.Errorf("unexpected Ollama authorization")
				}
				var body map[string]json.RawMessage
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if string(body["stream"]) != "false" {
					t.Errorf("streaming enabled")
				}
				if calls == 1 {
					if _, ok := body["tools"]; !ok {
						t.Errorf("missing tools")
					}
					if provider == "groq" {
						w.Write([]byte(`{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"get_pods","arguments":"{\"workload\":\"frontend\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":10,"completion_tokens":4}}`))
					} else {
						w.Write([]byte(`{"message":{"role":"assistant","tool_calls":[{"function":{"name":"get_pods","arguments":{"workload":"frontend"}}}]},"done":true,"done_reason":"stop","prompt_eval_count":10,"eval_count":4}`))
					}
					return
				}
				var messages []map[string]json.RawMessage
				if err := json.Unmarshal(body["messages"], &messages); err != nil {
					t.Error(err)
				}
				if len(messages) != 3 || string(messages[2]["role"]) != `"tool"` {
					t.Errorf("tool result not forwarded: %s", body["messages"])
				}
				if provider == "groq" {
					var prior []map[string]json.RawMessage
					if err := json.Unmarshal(messages[1]["tool_calls"], &prior); err != nil {
						t.Error(err)
					}
					var fn map[string]json.RawMessage
					json.Unmarshal(prior[0]["function"], &fn)
					if string(fn["arguments"]) != `"{\"workload\":\"frontend\"}"` {
						t.Errorf("Groq arguments are not a JSON string: %s", fn["arguments"])
					}
					w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}],"usage":{"prompt_tokens":15,"completion_tokens":2}}`))
				} else {
					if string(messages[2]["tool_name"]) != `"get_pods"` {
						t.Errorf("Ollama tool name missing")
					}
					w.Write([]byte(`{"message":{"role":"assistant","content":"done"},"done":true,"done_reason":"stop"}`))
				}
			}))
			defer server.Close()
			p := &httpProvider{config: Config{Provider: provider, Model: "test-model", APIKey: "test-secret", Endpoint: server.URL}, client: server.Client()}
			if provider == "ollama" {
				p.config.APIKey = ""
			}
			req := sampleRequest()
			first, err := p.Chat(context.Background(), req)
			if err != nil {
				t.Fatal(err)
			}
			if len(first.Message.ToolCalls) != 1 || first.Message.ToolCalls[0].Name != "get_pods" || first.Usage.InputTokens != 10 {
				t.Fatalf("unexpected first completion: %+v", first)
			}
			req.Messages = append(req.Messages, first.Message, Message{Role: RoleTool, ToolCallID: first.Message.ToolCalls[0].ID, ToolName: "get_pods", Content: `{"pods":[]}`})
			req.ToolChoice = ToolAuto
			second, err := p.Chat(context.Background(), req)
			if err != nil || second.Message.Content != "done" {
				t.Fatalf("unexpected follow-up: %+v %v", second, err)
			}
		})
	}
}

func TestRejectsUnsafeOrIncompleteOutput(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want string
	}{
		{"unknown tool", `{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"x","type":"function","function":{"name":"run_shell","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`, "invalid_response_unadvertised_tool"},
		{"missing required call", `{"choices":[{"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}]}`, "invalid_response_required_tool_call_missing"},
		{"truncated", `{"choices":[{"message":{"role":"assistant","content":"partial"},"finish_reason":"length"}]}`, "invalid_response_completion_truncated"},
		{"invalid arguments", `{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"x","type":"function","function":{"name":"get_pods","arguments":"[]"}}]},"finish_reason":"tool_calls"}]}`, "invalid_response_invalid_tool_arguments"},
		{"malformed completion", `{`, "invalid_response_malformed_completion"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte(tc.body)) }))
			defer server.Close()
			p := &httpProvider{config: Config{Provider: "groq", Model: "test-model", APIKey: "secret", Endpoint: server.URL}, client: server.Client()}
			_, err := p.Chat(context.Background(), sampleRequest())
			if !errors.Is(err, ErrInvalidResponse) {
				t.Fatalf("expected invalid response, got %v", err)
			}
			if got := FailureCode(err); got != tc.want {
				t.Fatalf("FailureCode() = %q, want %q", got, tc.want)
			}
		})
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte("secret diagnostic"))
	}))
	defer server.Close()
	p := &httpProvider{config: Config{Provider: "groq", Model: "test-model", APIKey: "secret", Endpoint: server.URL}, client: server.Client()}
	_, err := p.Chat(context.Background(), sampleRequest())
	if err == nil || strings.Contains(err.Error(), "secret diagnostic") {
		t.Fatalf("provider error exposed response body: %v", err)
	}
	if got := FailureCode(err); got != "provider_http_401" {
		t.Fatalf("unexpected safe provider error category: %s", got)
	}
}

func TestFailureCodeNeverIncludesUntrustedErrorText(t *testing.T) {
	tests := []struct {
		err  error
		want string
	}{
		{ProviderHTTPError{StatusCode: 429}, "provider_http_429"},
		{ErrTransport, "transport"},
		{ErrInvalidRequest, "invalid_request"},
		{ErrInvalidResponse, "invalid_response"},
		{context.DeadlineExceeded, "timeout"},
		{context.Canceled, "canceled"},
		{errors.New("api key secret diagnostic"), "unknown"},
	}
	for _, tc := range tests {
		if got := FailureCode(tc.err); got != tc.want {
			t.Errorf("FailureCode(%T) = %q, want %q", tc.err, got, tc.want)
		}
	}
}

func TestGroqReasoningConfiguration(t *testing.T) {
	values := map[string]string{"INCIDENTPILOT_LLM_PROVIDER": "groq", "INCIDENTPILOT_LLM_MODEL": "openai/gpt-oss-20b", "INCIDENTPILOT_LLM_API_KEY": "test-secret"}
	cfg, err := ParseConfig(func(key string) string { return values[key] })
	if err != nil || cfg.GroqReasoningEffort != "low" {
		t.Fatalf("expected low-effort GPT-OSS default, got %+v %v", cfg, err)
	}
	values["INCIDENTPILOT_GROQ_REASONING_EFFORT"] = "high"
	cfg, err = ParseConfig(func(key string) string { return values[key] })
	if err != nil || cfg.GroqReasoningEffort != "high" {
		t.Fatalf("valid override rejected: %+v %v", cfg, err)
	}
	values["INCIDENTPILOT_GROQ_REASONING_EFFORT"] = "none"
	if _, err := ParseConfig(func(key string) string { return values[key] }); err == nil {
		t.Fatal("unsupported GPT-OSS reasoning effort accepted")
	}
}

func TestSchemaTranslatedWithoutTools(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","additionalProperties":false,"required":["cause"],"properties":{"cause":{"type":"string"}}}`)
	for _, provider := range []string{"groq", "ollama"} {
		t.Run(provider, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]json.RawMessage
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if _, ok := body["tools"]; ok {
					t.Error("schema call included tools")
				}
				if provider == "groq" {
					var format struct {
						Type   string `json:"type"`
						Schema struct {
							Strict bool            `json:"strict"`
							Schema json.RawMessage `json:"schema"`
						} `json:"json_schema"`
					}
					if err := json.Unmarshal(body["response_format"], &format); err != nil || format.Type != "json_schema" || !format.Schema.Strict || string(format.Schema.Schema) != string(schema) {
						t.Errorf("invalid Groq strict schema: %s %v", body["response_format"], err)
					}
					if string(body["reasoning_effort"]) != `"low"` {
						t.Errorf("missing low reasoning effort: %s", body["reasoning_effort"])
					}
					w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"{\"cause\":\"unknown\"}"},"finish_reason":"stop"}]}`))
				} else {
					if string(body["format"]) != string(schema) {
						t.Errorf("invalid Ollama schema: %s", body["format"])
					}
					w.Write([]byte(`{"message":{"role":"assistant","content":"{\"cause\":\"unknown\"}"},"done":true,"done_reason":"stop"}`))
				}
			}))
			defer server.Close()
			cfg := Config{Provider: provider, Model: "openai/gpt-oss-20b", APIKey: "test-secret", Endpoint: server.URL, GroqReasoningEffort: "low"}
			if provider == "ollama" {
				cfg.APIKey, cfg.GroqReasoningEffort = "", ""
			}
			p := &httpProvider{config: cfg, client: server.Client()}
			_, err := p.Chat(context.Background(), ChatRequest{Messages: []Message{{Role: RoleUser, Content: "analyze"}}, JSONOutput: true, JSONSchema: schema, MaxTokens: 1536})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
	p := &httpProvider{config: Config{Provider: "groq", Model: "unsupported-model", APIKey: "test-secret"}, client: http.DefaultClient}
	_, err := p.Chat(context.Background(), ChatRequest{Messages: []Message{{Role: RoleUser, Content: "analyze"}}, JSONOutput: true, JSONSchema: schema})
	if !errors.Is(err, ErrUnsupportedCapability) {
		t.Fatalf("unsupported strict schema silently degraded: %v", err)
	}
}

func TestGroqErrorsAreBoundedAndRetryableOnlyWhenDirected(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/bad" {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"error":{"type":"invalid_request_error","message":"secret token=do-not-log","failed_generation":{"attempted_arguments":"secret"}}}`))
			return
		}
		w.Header().Set("Retry-After", "2")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()
	for _, tc := range []struct {
		path, want string
		retry      time.Duration
	}{{"/bad", "provider_http_400_tool_generation_failed", 0}, {"/limited", "provider_http_429", 2 * time.Second}} {
		p := &httpProvider{config: Config{Provider: "groq", Model: "test-model", APIKey: "test-secret", Endpoint: server.URL + tc.path}, client: server.Client()}
		_, err := p.Chat(context.Background(), sampleRequest())
		var httpErr ProviderHTTPError
		if !errors.As(err, &httpErr) || FailureCode(err) != tc.want || httpErr.RetryAfter != tc.retry || strings.Contains(err.Error(), "secret") {
			t.Fatalf("unsafe or incorrect Groq error: %v, code=%s", err, FailureCode(err))
		}
	}
}
