package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/itwanger/zcoder-go/internal/llm"
	"github.com/itwanger/zcoder-go/internal/tools"
)

func TestFormatToolCallPrettyJSON(t *testing.T) {
	args := json.RawMessage(`{"path":"README.md","limit":5}`)
	got := formatToolCall(llm.ToolCall{
		ID: "call_1",
		Function: llm.FunctionCall{
			Name:      "read_file",
			Arguments: args,
		},
	})

	if !strings.Contains(got, `"path": "README.md"`) {
		t.Fatalf("formatted tool call missing path: %q", got)
	}
	if !strings.Contains(got, `"limit": 5`) {
		t.Fatalf("formatted tool call missing limit: %q", got)
	}
}

func TestRepeatedToolCall(t *testing.T) {
	call := llm.ToolCall{
		ID: "call_1",
		Function: llm.FunctionCall{
			Name:      "web_search",
			Arguments: json.RawMessage(`{"query":"沉默王二"}`),
		},
	}
	seen := map[string]int{}
	if repeatedToolCall([]llm.ToolCall{call}, seen) {
		t.Fatal("first tool call should not be repeated")
	}
	if !repeatedToolCall([]llm.ToolCall{call}, seen) {
		t.Fatal("second identical tool call should be repeated")
	}
}

func TestParseRunCommand(t *testing.T) {
	mode, prompt := ParseRunCommand(" /plan refactor this package ")
	if mode != RunModePlan {
		t.Fatalf("mode = %q, want plan", mode)
	}
	if prompt != "refactor this package" {
		t.Fatalf("prompt = %q", prompt)
	}

	mode, prompt = ParseRunCommand("/team compare options")
	if mode != RunModeTeam {
		t.Fatalf("mode = %q, want team", mode)
	}
	if prompt != "compare options" {
		t.Fatalf("prompt = %q", prompt)
	}

	mode, prompt = ParseRunCommand("plain request")
	if mode != RunModeReact {
		t.Fatalf("mode = %q, want react", mode)
	}
	if prompt != "plain request" {
		t.Fatalf("prompt = %q", prompt)
	}
}

func TestRunCommandPlanAndExecute(t *testing.T) {
	client := &fakeClient{
		streamResponses: []llm.ChatResponse{
			{Content: "1. Inspect the code\n2. Apply the fix"},
			{Content: "fixed"},
		},
	}
	ag := New(client, tools.NewRegistry(t.TempDir(), tools.Options{}), nil, nil)

	got, err := ag.RunCommand(context.Background(), "/plan implement the change")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "Plan:\n1. Inspect the code") {
		t.Fatalf("answer should include plan, got %q", got)
	}
	if !strings.Contains(got, "Result:\nfixed") {
		t.Fatalf("answer should include result, got %q", got)
	}
	if len(client.streamTools) != 2 {
		t.Fatalf("stream calls = %d, want 2", len(client.streamTools))
	}
	if len(client.streamTools[0]) != 0 {
		t.Fatalf("planning call should not expose tools")
	}
	if len(client.streamTools[1]) == 0 {
		t.Fatalf("execution call should expose tools")
	}
	userPrompt := client.streamMessages[1][len(client.streamMessages[1])-1].Content
	if !strings.Contains(userPrompt, "Plan approved.") || !strings.Contains(userPrompt, "Original request:\nimplement the change") {
		t.Fatalf("execution prompt did not include plan and original request: %q", userPrompt)
	}
}

func TestRunModeRejectsUnknownMode(t *testing.T) {
	ag := New(&fakeClient{}, tools.NewRegistry(t.TempDir(), tools.Options{}), nil, nil)
	if _, err := ag.RunMode(context.Background(), RunMode("unknown"), "task"); err == nil {
		t.Fatal("expected unknown mode error")
	}
}

type fakeClient struct {
	streamResponses []llm.ChatResponse
	chatResponses   []llm.ChatResponse
	streamMessages  [][]llm.Message
	streamTools     [][]llm.Tool
}

func (f *fakeClient) Chat(ctx context.Context, messages []llm.Message, tools []llm.Tool) (llm.ChatResponse, error) {
	if len(f.chatResponses) == 0 {
		return llm.ChatResponse{}, errors.New("unexpected Chat call")
	}
	resp := f.chatResponses[0]
	f.chatResponses = f.chatResponses[1:]
	return resp, nil
}

func (f *fakeClient) ChatStream(ctx context.Context, messages []llm.Message, tools []llm.Tool, observe llm.StreamObserver) (llm.ChatResponse, error) {
	f.streamMessages = append(f.streamMessages, append([]llm.Message(nil), messages...))
	f.streamTools = append(f.streamTools, append([]llm.Tool(nil), tools...))
	if len(f.streamResponses) == 0 {
		return llm.ChatResponse{}, errors.New("unexpected ChatStream call")
	}
	resp := f.streamResponses[0]
	f.streamResponses = f.streamResponses[1:]
	if observe != nil && resp.Content != "" {
		observe(llm.StreamEvent{Type: llm.StreamContentDelta, Delta: resp.Content})
	}
	return resp, nil
}

func (f *fakeClient) Provider() string            { return "test" }
func (f *fakeClient) Model() string               { return "test-model" }
func (f *fakeClient) MaxContext() int             { return 128000 }
func (f *fakeClient) SupportsTools() bool         { return true }
func (f *fakeClient) SupportsImageInput() bool    { return false }
func (f *fakeClient) SupportsPromptCaching() bool { return false }
