package llm

import (
	"context"
	"encoding/json"
)

type Client interface {
	Chat(ctx context.Context, messages []Message, tools []Tool) (ChatResponse, error)
	ChatStream(ctx context.Context, messages []Message, tools []Tool, observe StreamObserver) (ChatResponse, error)
	Provider() string
	Model() string
	MaxContext() int
	SupportsTools() bool
	SupportsImageInput() bool
	SupportsPromptCaching() bool
}

type ContentPart struct {
	Type        string `json:"type"`
	Text        string `json:"text,omitempty"`
	ImageBase64 string `json:"image_base64,omitempty"`
	ImageURL    string `json:"image_url,omitempty"`
	MimeType    string `json:"mime_type,omitempty"`
}

type Message struct {
	Role       string        `json:"role"`
	Content    string        `json:"content,omitempty"`
	Name       string        `json:"name,omitempty"`
	Parts      []ContentPart `json:"parts,omitempty"`
	ToolCalls  []ToolCall    `json:"tool_calls,omitempty"`
	ToolCallID string        `json:"tool_call_id,omitempty"`
}

type ToolCall struct {
	ID       string       `json:"id"`
	Function FunctionCall `json:"function"`
}

type FunctionCall struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

type ChatResponse struct {
	Content           string
	ReasoningContent  string
	ToolCalls         []ToolCall
	InputTokens       int
	OutputTokens      int
	CachedInputTokens int
}

type StreamEventType string

const (
	StreamReasoningDelta StreamEventType = "reasoning_delta"
	StreamContentDelta   StreamEventType = "content_delta"
)

type StreamEvent struct {
	Type  StreamEventType
	Delta string
}

type StreamObserver func(StreamEvent)

func System(content string) Message {
	return Message{Role: "system", Content: content}
}

func User(content string) Message {
	return Message{Role: "user", Content: content}
}

func Assistant(content string) Message {
	return Message{Role: "assistant", Content: content}
}

func AssistantWithTools(content string, calls []ToolCall) Message {
	return Message{Role: "assistant", Content: content, ToolCalls: calls}
}

func ToolResult(id, name, content string) Message {
	return Message{Role: "tool", ToolCallID: id, Name: name, Content: content}
}
