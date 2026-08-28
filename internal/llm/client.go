package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/itwanger/zcoder-go/internal/config"
)

type OpenAICompatibleClient struct {
	provider string
	cfg      config.ProviderConfig
	http     *http.Client
}

func NewClient(cfg config.Config) (Client, error) {
	provider := strings.ToLower(cfg.DefaultProvider)
	p := cfg.Provider(provider)
	if p.APIKey == "" && provider != "freellmapi" {
		return nil, fmt.Errorf("missing API key for provider %s; run `zcoder doctor` for configuration details", provider)
	}
	if p.BaseURL == "" || p.Model == "" {
		return nil, fmt.Errorf("provider %s is missing base_url or model", provider)
	}
	return &OpenAICompatibleClient{
		provider: provider,
		cfg:      p,
		http:     &http.Client{Timeout: 180 * time.Second},
	}, nil
}

func (c *OpenAICompatibleClient) Provider() string { return c.provider }
func (c *OpenAICompatibleClient) Model() string    { return c.cfg.Model }
func (c *OpenAICompatibleClient) MaxContext() int {
	if c.cfg.MaxContext > 0 {
		return c.cfg.MaxContext
	}
	return 128000
}
func (c *OpenAICompatibleClient) SupportsTools() bool         { return true }
func (c *OpenAICompatibleClient) SupportsImageInput() bool    { return c.cfg.SupportsImages }
func (c *OpenAICompatibleClient) SupportsPromptCaching() bool { return c.cfg.SupportsCaching }

func (c *OpenAICompatibleClient) Chat(ctx context.Context, messages []Message, tools []Tool) (ChatResponse, error) {
	resp, err := c.chatRequest(ctx, messages, tools, false)
	if err != nil {
		return ChatResponse{}, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return ChatResponse{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ChatResponse{}, fmt.Errorf("llm request failed: %s: %s", resp.Status, string(data))
	}
	var out openAIChatResponse
	if err := json.Unmarshal(data, &out); err != nil {
		return ChatResponse{}, err
	}
	if len(out.Choices) == 0 {
		return ChatResponse{}, fmt.Errorf("llm returned no choices")
	}
	msg := out.Choices[0].Message
	return ChatResponse{
		Content:           msg.Content,
		ReasoningContent:  msg.ReasoningContent,
		ToolCalls:         msg.toToolCalls(),
		InputTokens:       out.Usage.PromptTokens,
		OutputTokens:      out.Usage.CompletionTokens,
		CachedInputTokens: out.Usage.PromptTokensDetails.CachedTokens,
	}, nil
}

func (c *OpenAICompatibleClient) ChatStream(ctx context.Context, messages []Message, tools []Tool, observe StreamObserver) (ChatResponse, error) {
	resp, err := c.chatRequest(ctx, messages, tools, true)
	if err != nil {
		return ChatResponse{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		return ChatResponse{}, fmt.Errorf("llm request failed: %s: %s", resp.Status, string(data))
	}

	var content strings.Builder
	var reasoning strings.Builder
	toolBuilders := map[int]*streamToolCallBuilder{}
	var usage struct {
		PromptTokens      int
		CompletionTokens  int
		CachedInputTokens int
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, ":") || !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}
		var chunk openAIStreamResponse
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return ChatResponse{}, fmt.Errorf("parse llm stream chunk: %w: %s", err, data)
		}
		if chunk.Usage.PromptTokens > 0 {
			usage.PromptTokens = chunk.Usage.PromptTokens
			usage.CompletionTokens = chunk.Usage.CompletionTokens
			usage.CachedInputTokens = chunk.Usage.PromptTokensDetails.CachedTokens
		}
		for _, choice := range chunk.Choices {
			delta := choice.Delta
			reasoningDelta := delta.ReasoningContent
			if reasoningDelta == "" {
				reasoningDelta = delta.Reasoning
			}
			if reasoningDelta != "" {
				reasoning.WriteString(reasoningDelta)
				emitStream(observe, StreamEvent{Type: StreamReasoningDelta, Delta: reasoningDelta})
			}
			if delta.Content != "" {
				content.WriteString(delta.Content)
				emitStream(observe, StreamEvent{Type: StreamContentDelta, Delta: delta.Content})
			}
			for _, call := range delta.ToolCalls {
				builder := toolBuilders[call.Index]
				if builder == nil {
					builder = &streamToolCallBuilder{index: call.Index}
					toolBuilders[call.Index] = builder
				}
				if call.ID != "" {
					builder.id = call.ID
				}
				if call.Function.Name != "" {
					builder.name += call.Function.Name
				}
				if call.Function.Arguments != "" {
					builder.arguments.WriteString(call.Function.Arguments)
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return ChatResponse{}, err
	}
	return ChatResponse{
		Content:           content.String(),
		ReasoningContent:  reasoning.String(),
		ToolCalls:         assembleStreamToolCalls(toolBuilders),
		InputTokens:       usage.PromptTokens,
		OutputTokens:      usage.CompletionTokens,
		CachedInputTokens: usage.CachedInputTokens,
	}, nil
}

func (c *OpenAICompatibleClient) chatRequest(ctx context.Context, messages []Message, tools []Tool, stream bool) (*http.Response, error) {
	body := map[string]any{
		"model":       c.cfg.Model,
		"messages":    c.openAIMessages(messages),
		"temperature": 0.2,
		"stream":      stream,
	}
	if len(tools) > 0 {
		body["tools"] = openAITools(tools)
		body["tool_choice"] = "auto"
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	url := strings.TrimRight(c.cfg.BaseURL, "/") + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	}
	return c.http.Do(req)
}

func (c *OpenAICompatibleClient) openAIMessages(messages []Message) []map[string]any {
	result := make([]map[string]any, 0, len(messages))
	for _, msg := range messages {
		m := map[string]any{"role": msg.Role}
		if msg.Name != "" {
			m["name"] = msg.Name
		}
		if msg.ToolCallID != "" {
			m["tool_call_id"] = msg.ToolCallID
		}
		if len(msg.ToolCalls) > 0 {
			calls := make([]map[string]any, 0, len(msg.ToolCalls))
			for _, call := range msg.ToolCalls {
				calls = append(calls, map[string]any{
					"id":   call.ID,
					"type": "function",
					"function": map[string]any{
						"name":      call.Function.Name,
						"arguments": string(call.Function.Arguments),
					},
				})
			}
			m["tool_calls"] = calls
		}
		if len(msg.Parts) > 0 && c.SupportsImageInput() {
			content := make([]map[string]any, 0, len(msg.Parts))
			for _, part := range msg.Parts {
				switch part.Type {
				case "text":
					content = append(content, map[string]any{"type": "text", "text": part.Text})
				case "image_url":
					content = append(content, map[string]any{"type": "image_url", "image_url": map[string]any{"url": part.ImageURL}})
				case "image_base64":
					mime := part.MimeType
					if mime == "" {
						mime = "image/png"
					}
					content = append(content, map[string]any{
						"type":      "image_url",
						"image_url": map[string]any{"url": "data:" + mime + ";base64," + part.ImageBase64},
					})
				}
			}
			m["content"] = content
		} else {
			m["content"] = msg.Content
		}
		result = append(result, m)
	}
	return result
}

func openAITools(tools []Tool) []map[string]any {
	result := make([]map[string]any, 0, len(tools))
	for _, tool := range tools {
		result = append(result, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        tool.Name,
				"description": tool.Description,
				"parameters":  tool.Parameters,
			},
		})
	}
	return result
}

type openAIChatResponse struct {
	Choices []struct {
		Message openAIMessage `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens        int `json:"prompt_tokens"`
		CompletionTokens    int `json:"completion_tokens"`
		PromptTokensDetails struct {
			CachedTokens int `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
	} `json:"usage"`
}

type openAIMessage struct {
	Content          string `json:"content"`
	ReasoningContent string `json:"reasoning_content"`
	ToolCalls        []struct {
		ID       string `json:"id"`
		Type     string `json:"type"`
		Function struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		} `json:"function"`
	} `json:"tool_calls"`
}

type openAIStreamResponse struct {
	Choices []struct {
		Delta openAIStreamDelta `json:"delta"`
	} `json:"choices"`
	Usage struct {
		PromptTokens        int `json:"prompt_tokens"`
		CompletionTokens    int `json:"completion_tokens"`
		PromptTokensDetails struct {
			CachedTokens int `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
	} `json:"usage"`
}

type openAIStreamDelta struct {
	Content          string `json:"content"`
	Reasoning        string `json:"reasoning"`
	ReasoningContent string `json:"reasoning_content"`
	ToolCalls        []struct {
		Index    int    `json:"index"`
		ID       string `json:"id"`
		Type     string `json:"type"`
		Function struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
	} `json:"tool_calls"`
}

type streamToolCallBuilder struct {
	index     int
	id        string
	name      string
	arguments strings.Builder
}

func (m openAIMessage) toToolCalls() []ToolCall {
	calls := make([]ToolCall, 0, len(m.ToolCalls))
	for _, call := range m.ToolCalls {
		calls = append(calls, ToolCall{
			ID: call.ID,
			Function: FunctionCall{
				Name:      call.Function.Name,
				Arguments: normalizeFunctionArguments(call.Function.Arguments),
			},
		})
	}
	return calls
}

func normalizeFunctionArguments(raw json.RawMessage) json.RawMessage {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return json.RawMessage(`{}`)
	}
	if trimmed[0] != '"' {
		return raw
	}
	var encoded string
	if err := json.Unmarshal(trimmed, &encoded); err != nil {
		return raw
	}
	encoded = strings.TrimSpace(encoded)
	if encoded == "" {
		return json.RawMessage(`{}`)
	}
	return json.RawMessage(encoded)
}

func assembleStreamToolCalls(builders map[int]*streamToolCallBuilder) []ToolCall {
	if len(builders) == 0 {
		return nil
	}
	indexes := make([]int, 0, len(builders))
	for index := range builders {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)
	calls := make([]ToolCall, 0, len(indexes))
	for _, index := range indexes {
		builder := builders[index]
		id := builder.id
		if id == "" {
			id = fmt.Sprintf("call_%d", index)
		}
		args := strings.TrimSpace(builder.arguments.String())
		if args == "" {
			args = "{}"
		}
		calls = append(calls, ToolCall{
			ID: id,
			Function: FunctionCall{
				Name:      builder.name,
				Arguments: normalizeFunctionArguments(json.RawMessage(args)),
			},
		})
	}
	return calls
}

func emitStream(observe StreamObserver, event StreamEvent) {
	if observe != nil && event.Delta != "" {
		observe(event)
	}
}
