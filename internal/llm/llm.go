// Package llm defines the provider-independent inference boundary.
package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

type ToolChoice string

const (
	ToolNone     ToolChoice = "none"
	ToolAuto     ToolChoice = "auto"
	ToolRequired ToolChoice = "required"
)

type ToolDefinition struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

type ToolCall struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type Message struct {
	Role       Role       `json:"role"`
	Content    string     `json:"content,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	ToolName   string     `json:"tool_name,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
}

type ChatRequest struct {
	Model      string           `json:"model"`
	Messages   []Message        `json:"messages"`
	Tools      []ToolDefinition `json:"tools,omitempty"`
	ToolChoice ToolChoice       `json:"tool_choice,omitempty"`
	JSONOutput bool             `json:"json_output,omitempty"`
	JSONSchema json.RawMessage  `json:"json_schema,omitempty"`
	MaxTokens  int              `json:"max_tokens,omitempty"`
}

type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

type ChatResponse struct {
	Message      Message `json:"message"`
	FinishReason string  `json:"finish_reason"`
	Usage        Usage   `json:"usage"`
}

type Provider interface {
	Chat(context.Context, ChatRequest) (ChatResponse, error)
}

var ErrInvalidRequest = errors.New("invalid LLM request")
var ErrInvalidResponse = errors.New("invalid LLM response")
var ErrTransport = errors.New("LLM transport failed")
var ErrUnsupportedCapability = errors.New("LLM provider does not support requested capability")

// invalidResponseError carries only an internal, fixed validation rule name.
// Never populate it from provider text or model output.
type invalidResponseError struct{ code string }

func (e invalidResponseError) Error() string { return ErrInvalidResponse.Error() + ": " + e.code }
func (e invalidResponseError) Unwrap() error { return ErrInvalidResponse }

// ProviderHTTPError retains only the HTTP status, never the provider response
// body, which may contain request content or other sensitive data.
type ProviderHTTPError struct {
	StatusCode int
	RetryAfter time.Duration
	detail     string
}

func (e ProviderHTTPError) Error() string {
	return fmt.Sprintf("LLM provider returned HTTP %d", e.StatusCode)
}

// FailureCode is a bounded, provider-independent diagnostic safe to persist
// in investigation reports. It never incorporates untrusted error text.
func FailureCode(err error) string {
	var httpErr ProviderHTTPError
	var responseErr invalidResponseError
	switch {
	case errors.As(err, &httpErr):
		if httpErr.StatusCode == 400 && httpErr.detail != "" {
			return "provider_http_400_" + httpErr.detail
		}
		return fmt.Sprintf("provider_http_%d", httpErr.StatusCode)
	case errors.As(err, &responseErr):
		return "invalid_response_" + responseErr.code
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, ErrTransport):
		return "transport"
	case errors.Is(err, ErrInvalidRequest):
		return "invalid_request"
	case errors.Is(err, ErrUnsupportedCapability):
		return "unsupported_capability"
	case errors.Is(err, ErrInvalidResponse):
		return "invalid_response"
	default:
		return "unknown"
	}
}

func validateRequest(req ChatRequest) error {
	if strings.TrimSpace(req.Model) == "" || len(req.Model) > 128 || len(req.Messages) == 0 || len(req.Messages) > 64 || len(req.Tools) > 16 || req.MaxTokens < 0 || req.MaxTokens > 8192 {
		return fmt.Errorf("%w: model, message, tool, or token limit", ErrInvalidRequest)
	}
	if req.ToolChoice != "" && req.ToolChoice != ToolNone && req.ToolChoice != ToolAuto && req.ToolChoice != ToolRequired {
		return fmt.Errorf("%w: unknown tool choice", ErrInvalidRequest)
	}
	if req.ToolChoice == ToolRequired && len(req.Tools) == 0 {
		return fmt.Errorf("%w: required tools are missing", ErrInvalidRequest)
	}
	if req.ToolChoice == ToolNone && len(req.Tools) > 0 {
		return fmt.Errorf("%w: tools supplied with none choice", ErrInvalidRequest)
	}
	if req.JSONOutput && len(req.Tools) > 0 {
		return fmt.Errorf("%w: JSON output and tool calling must use separate turns", ErrInvalidRequest)
	}
	if len(req.JSONSchema) > 0 && (!req.JSONOutput || !jsonObject(req.JSONSchema)) {
		return fmt.Errorf("%w: JSON schema requires structured JSON output", ErrInvalidRequest)
	}
	seen := make(map[string]bool, len(req.Tools))
	for _, tool := range req.Tools {
		if tool.Name == "" || len(tool.Name) > 64 || seen[tool.Name] || !jsonObject(tool.Parameters) {
			return fmt.Errorf("%w: invalid or duplicate tool definition", ErrInvalidRequest)
		}
		seen[tool.Name] = true
	}
	for _, message := range req.Messages {
		switch message.Role {
		case RoleSystem, RoleUser:
			if message.Content == "" || message.ToolCallID != "" || len(message.ToolCalls) != 0 {
				return fmt.Errorf("%w: invalid system or user message", ErrInvalidRequest)
			}
		case RoleAssistant:
			if message.Content == "" && len(message.ToolCalls) == 0 {
				return fmt.Errorf("%w: empty assistant message", ErrInvalidRequest)
			}
			for _, call := range message.ToolCalls {
				if call.ID == "" || call.Name == "" || !jsonObject(call.Arguments) {
					return fmt.Errorf("%w: invalid prior tool call", ErrInvalidRequest)
				}
			}
		case RoleTool:
			if message.ToolCallID == "" || message.ToolName == "" || message.Content == "" || len(message.ToolCalls) != 0 {
				return fmt.Errorf("%w: invalid tool result", ErrInvalidRequest)
			}
		default:
			return fmt.Errorf("%w: unknown role", ErrInvalidRequest)
		}
	}
	return nil
}

func validateResponse(req ChatRequest, res ChatResponse) error {
	if res.Message.Role != RoleAssistant || (res.Message.Content == "" && len(res.Message.ToolCalls) == 0) {
		return invalidResponseError{"missing_assistant_output"}
	}
	if req.ToolChoice == ToolRequired && len(res.Message.ToolCalls) == 0 {
		return invalidResponseError{"required_tool_call_missing"}
	}
	if (req.ToolChoice == ToolNone || len(req.Tools) == 0) && len(res.Message.ToolCalls) > 0 {
		return invalidResponseError{"unexpected_tool_call"}
	}
	allowed := make(map[string]bool, len(req.Tools))
	seenIDs := make(map[string]bool, len(res.Message.ToolCalls))
	for _, tool := range req.Tools {
		allowed[tool.Name] = true
	}
	for _, call := range res.Message.ToolCalls {
		if call.ID == "" || seenIDs[call.ID] {
			return invalidResponseError{"invalid_tool_call_id"}
		}
		if !allowed[call.Name] {
			return invalidResponseError{"unadvertised_tool"}
		}
		if !jsonObject(call.Arguments) {
			return invalidResponseError{"invalid_tool_arguments"}
		}
		seenIDs[call.ID] = true
	}
	if req.JSONOutput && (len(res.Message.ToolCalls) > 0 || !jsonObject(json.RawMessage(res.Message.Content))) {
		return invalidResponseError{"structured_json_missing"}
	}
	return nil
}

func jsonObject(raw json.RawMessage) bool {
	var object map[string]json.RawMessage
	return len(raw) > 0 && json.Unmarshal(raw, &object) == nil && object != nil
}
