package llm

import (
	"encoding/json"
	"fmt"
)

type ollamaCall struct {
	Function functionWire `json:"function"`
}

type ollamaMessage struct {
	Role      Role         `json:"role"`
	Content   string       `json:"content,omitempty"`
	ToolName  string       `json:"tool_name,omitempty"`
	ToolCalls []ollamaCall `json:"tool_calls,omitempty"`
}

func ollamaRequest(req ChatRequest) any {
	messages := make([]ollamaMessage, 0, len(req.Messages))
	for _, msg := range req.Messages {
		out := ollamaMessage{Role: msg.Role, Content: msg.Content, ToolName: msg.ToolName}
		for _, call := range msg.ToolCalls {
			out.ToolCalls = append(out.ToolCalls, ollamaCall{Function: functionWire{Name: call.Name, Arguments: call.Arguments}})
		}
		messages = append(messages, out)
	}
	var format any
	if req.JSONOutput {
		format = "json"
		if len(req.JSONSchema) > 0 {
			format = req.JSONSchema
		}
	}
	options := map[string]int{}
	if req.MaxTokens > 0 {
		options["num_predict"] = req.MaxTokens
	}
	return struct {
		Model    string          `json:"model"`
		Messages []ollamaMessage `json:"messages"`
		Tools    []toolWire      `json:"tools,omitempty"`
		Format   any             `json:"format,omitempty"`
		Options  map[string]int  `json:"options,omitempty"`
		Stream   bool            `json:"stream"`
	}{req.Model, messages, wireTools(req.Tools), format, options, false}
}

func parseOllama(body []byte) (ChatResponse, error) {
	var raw struct {
		Message         ollamaMessage `json:"message"`
		Done            bool          `json:"done"`
		DoneReason      string        `json:"done_reason"`
		PromptEvalCount int           `json:"prompt_eval_count"`
		EvalCount       int           `json:"eval_count"`
	}
	if err := json.Unmarshal(body, &raw); err != nil || !raw.Done || raw.Message.Role != RoleAssistant || (raw.DoneReason != "" && raw.DoneReason != "stop") {
		return ChatResponse{}, fmt.Errorf("%w: incomplete Ollama completion", ErrInvalidResponse)
	}
	message := Message{Role: RoleAssistant, Content: raw.Message.Content}
	for i, call := range raw.Message.ToolCalls {
		message.ToolCalls = append(message.ToolCalls, ToolCall{ID: fmt.Sprintf("ollama_call_%d", i), Name: call.Function.Name, Arguments: call.Function.Arguments})
	}
	return ChatResponse{Message: message, FinishReason: raw.DoneReason, Usage: Usage{raw.PromptEvalCount, raw.EvalCount}}, nil
}
