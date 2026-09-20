// Package llmplan is a language-model planner that proposes bookings through
// tools. Policy, not the model, holds authority: propose_booking only forwards a
// proposal to the mission authority, which applies the flight rules and the trust
// tier (master plan §7 rev 3). The model sees structured fields only; hostile
// station free text is stripped. On any error or timeout the greedy plan runs, so
// no pass is missed because a model was slow.
package llmplan

import (
	"context"
	"encoding/json"
)

// Model is the Messages API surface llmplan needs: one turn of a tool-use loop.
// The real client is Client; tests supply a fake. The wire shapes below mirror
// the current Anthropic Messages API (POST /v1/messages, anthropic-version
// 2023-06-01), verified against the docs at build time (hard rule 9).
type Model interface {
	Create(ctx context.Context, req MessageRequest) (MessageResponse, error)
}

// MessageRequest is the /v1/messages request body.
type MessageRequest struct {
	Model      string      `json:"model"`
	MaxTokens  int         `json:"max_tokens"`
	System     string      `json:"system,omitempty"`
	Tools      []Tool      `json:"tools,omitempty"`
	ToolChoice *ToolChoice `json:"tool_choice,omitempty"`
	Messages   []Message   `json:"messages"`
}

// Tool is one client tool definition. InputSchema is a JSON Schema object.
type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"input_schema"`
}

// ToolChoice controls tool calling; the planner sets auto with parallel disabled
// so each turn carries at most one tool_use.
type ToolChoice struct {
	Type                   string `json:"type"`
	DisableParallelToolUse bool   `json:"disable_parallel_tool_use,omitempty"`
}

// Message is one conversation turn.
type Message struct {
	Role    string  `json:"role"`
	Content []Block `json:"content"`
}

// Block is a content block. Fields are a union across text, tool_use and
// tool_result; only those relevant to Type are populated.
type Block struct {
	Type string `json:"type"`
	// text
	Text string `json:"text,omitempty"`
	// tool_use
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`
	// tool_result
	ToolUseID string `json:"tool_use_id,omitempty"`
	Content   string `json:"content,omitempty"`
	IsError   bool   `json:"is_error,omitempty"`
}

// MessageResponse is the /v1/messages response.
type MessageResponse struct {
	Content    []Block `json:"content"`
	StopReason string  `json:"stop_reason"`
}

func textBlock(s string) Block            { return Block{Type: "text", Text: s} }
func toolResult(id, out string) Block     { return Block{Type: "tool_result", ToolUseID: id, Content: out} }
func userMessage(bs ...Block) Message     { return Message{Role: "user", Content: bs} }
func assistantMessage(bs []Block) Message { return Message{Role: "assistant", Content: bs} }
