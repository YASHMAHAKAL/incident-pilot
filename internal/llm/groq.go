package llm

import (
	"encoding/json"
)

type functionWire struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
	Arguments   json.RawMessage `json:"arguments,omitempty"`
}

type toolWire struct {
	Type     string       `json:"type"`
	Function functionWire `json:"function"`
}

type groqCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type groqMessage struct {
	Role       Role       `json:"role"`
	Content    string     `json:"content,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	ToolCalls  []groqCall `json:"tool_calls,omitempty"`
}

func wireTools(tools []ToolDefinition) []toolWire {
	result := make([]toolWire, 0, len(tools))
	for _, tool := range tools {
		result = append(result, toolWire{Type: "function", Function: functionWire{Name: tool.Name, Description: tool.Description, Parameters: tool.Parameters}})
	}
	return result
}

func groqStrictSchemaSupported(model string) bool {
	return model == "openai/gpt-oss-20b" || model == "openai/gpt-oss-120b" || model == "qwen/qwen3.8-27b"
}

func groqRequest(req ChatRequest, cfg Config) any {
	messages := make([]groqMessage, 0, len(req.Messages))
	for _, msg := range req.Messages {
		out := groqMessage{Role: msg.Role, Content: msg.Content, ToolCallID: msg.ToolCallID}
		for _, call := range msg.ToolCalls {
			wire := groqCall{ID: call.ID, Type: "function"}
			wire.Function.Name = call.Name
			wire.Function.Arguments = string(call.Arguments)
			out.ToolCalls = append(out.ToolCalls, wire)
		}
		messages = append(messages, out)
	}
	choice := req.ToolChoice
	if choice == "" && len(req.Tools) > 0 {
		choice = ToolAuto
	}
	format := any(nil)
	if req.JSONOutput {
		format = map[string]string{"type": "json_object"}
		if len(req.JSONSchema) > 0 {
			format = map[string]any{"type": "json_schema", "json_schema": map[string]any{"name": "incident_analysis", "strict": true, "schema": req.JSONSchema}}
		}
	}
	var parallel *bool
	if len(req.Tools) > 0 {
		parallel = new(bool)
	}
	return struct {
		Model             string        `json:"model"`
		Messages          []groqMessage `json:"messages"`
		Tools             []toolWire    `json:"tools,omitempty"`
		ToolChoice        ToolChoice    `json:"tool_choice,omitempty"`
		ResponseFormat    any           `json:"response_format,omitempty"`
		MaxTokens         int           `json:"max_completion_tokens,omitempty"`
		ReasoningEffort   string        `json:"reasoning_effort,omitempty"`
		ParallelToolCalls *bool         `json:"parallel_tool_calls,omitempty"`
		Stream            bool          `json:"stream"`
	}{req.Model, messages, wireTools(req.Tools), choice, format, req.MaxTokens, cfg.GroqReasoningEffort, parallel, false}
}

func parseGroq(body []byte) (ChatResponse, error) {
	var raw struct {
		Choices []struct {
			Message      groqMessage `json:"message"`
			FinishReason string      `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			Prompt     int `json:"prompt_tokens"`
			Completion int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &raw); err != nil || len(raw.Choices) != 1 {
		return ChatResponse{}, invalidResponseError{"malformed_completion"}
	}
	choice := raw.Choices[0]
	if choice.Message.Role != RoleAssistant {
		return ChatResponse{}, invalidResponseError{"invalid_assistant_role"}
	}
	if choice.FinishReason == "length" {
		return ChatResponse{}, invalidResponseError{"completion_truncated"}
	}
	if choice.FinishReason != "stop" && choice.FinishReason != "tool_calls" {
		return ChatResponse{}, invalidResponseError{"unexpected_finish_reason"}
	}
	message := Message{Role: RoleAssistant, Content: choice.Message.Content}
	for _, call := range choice.Message.ToolCalls {
		if call.Type != "function" {
			return ChatResponse{}, invalidResponseError{"unsupported_tool_type"}
		}
		message.ToolCalls = append(message.ToolCalls, ToolCall{ID: call.ID, Name: call.Function.Name, Arguments: json.RawMessage(call.Function.Arguments)})
	}
	return ChatResponse{Message: message, FinishReason: choice.FinishReason, Usage: Usage{raw.Usage.Prompt, raw.Usage.Completion}}, nil
}
