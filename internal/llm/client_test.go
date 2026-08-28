package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/itwanger/zcoder-go/internal/config"
)

func TestNormalizeFunctionArgumentsFromJSONString(t *testing.T) {
	raw := json.RawMessage(`"{\"path\":\"README.md\",\"limit\":5}"`)
	got := normalizeFunctionArguments(raw)
	want := `{"path":"README.md","limit":5}`
	if string(got) != want {
		t.Fatalf("normalizeFunctionArguments = %s, want %s", got, want)
	}
}

func TestNormalizeFunctionArgumentsFromObject(t *testing.T) {
	raw := json.RawMessage(`{"path":"README.md"}`)
	got := normalizeFunctionArguments(raw)
	if string(got) != string(raw) {
		t.Fatalf("object arguments should be unchanged, got %s", got)
	}
}

func TestOpenAIMessagesIncludesToolName(t *testing.T) {
	client := OpenAICompatibleClient{}
	got := client.openAIMessages([]Message{
		ToolResult("call_1", "read_file", "content"),
	})
	if got[0]["name"] != "read_file" {
		t.Fatalf("tool result name = %v, want read_file", got[0]["name"])
	}
	if got[0]["tool_call_id"] != "call_1" {
		t.Fatalf("tool_call_id = %v, want call_1", got[0]["tool_call_id"])
	}
}

func TestChatStreamParsesSSE(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		chunks := []string{
			`{"choices":[{"delta":{"reasoning_content":"先搜索"}}]}`,
			`{"choices":[{"delta":{"content":"好的"}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"web_search","arguments":"{\"query\":"}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"沉默王二\"}"}}]}}]}`,
			`[DONE]`,
		}
		for _, chunk := range chunks {
			fmt.Fprintf(w, "data: %s\n\n", chunk)
		}
	}))
	defer server.Close()

	client := OpenAICompatibleClient{
		cfg: config.ProviderConfig{
			BaseURL: server.URL,
			Model:   "test-model",
		},
		http: server.Client(),
	}
	var events []StreamEvent
	resp, err := client.ChatStream(context.Background(), []Message{User("hi")}, nil, func(event StreamEvent) {
		events = append(events, event)
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ReasoningContent != "先搜索" {
		t.Fatalf("reasoning = %q", resp.ReasoningContent)
	}
	if resp.Content != "好的" {
		t.Fatalf("content = %q", resp.Content)
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("tool calls = %d, want 1", len(resp.ToolCalls))
	}
	if got := string(resp.ToolCalls[0].Function.Arguments); got != `{"query":"沉默王二"}` {
		t.Fatalf("tool args = %s", got)
	}
	if len(events) != 2 {
		t.Fatalf("events = %d, want 2", len(events))
	}
}
