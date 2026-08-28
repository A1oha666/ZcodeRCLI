package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/itwanger/zcoder-go/internal/agent"
	"github.com/itwanger/zcoder-go/internal/llm"
	"github.com/itwanger/zcoder-go/internal/tools"
)

func TestTurnHandlerSupportsPlanMode(t *testing.T) {
	client := &fakeClient{
		streamResponses: []llm.ChatResponse{
			{Content: "1. Plan the task"},
			{Content: "done"},
		},
	}
	ag := agent.New(client, tools.NewRegistry(t.TempDir(), tools.Options{}), nil, nil)
	thread := &Thread{ID: "thread-test"}
	server := NewServer("127.0.0.1:0", "test-key", func() *agent.Agent { return ag })

	body := bytes.NewBufferString(`{"input":"ship this","mode":"plan"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/threads/thread-test/turns", body)
	rec := httptest.NewRecorder()
	server.turnHandler(rec, req, thread)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	events := waitForEvents(t, thread, "turn.completed")
	var delta string
	for _, event := range events {
		if event.Type != "message.delta" {
			continue
		}
		data, ok := event.Data.(map[string]string)
		if !ok {
			t.Fatalf("message.delta data = %#v", event.Data)
		}
		delta = data["delta"]
	}
	if !strings.Contains(delta, "Plan:\n1. Plan the task") || !strings.Contains(delta, "Result:\ndone") {
		t.Fatalf("delta should include plan and result, got %q", delta)
	}
}

func TestTurnHandlerRejectsUnknownMode(t *testing.T) {
	server := NewServer("127.0.0.1:0", "test-key", func() *agent.Agent { return nil })
	thread := &Thread{ID: "thread-test"}

	body := bytes.NewBufferString(`{"input":"ship this","mode":"unknown"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/threads/thread-test/turns", body)
	rec := httptest.NewRecorder()
	server.turnHandler(rec, req, thread)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "mode must be one of") {
		t.Fatalf("body = %q", rec.Body.String())
	}
}

func waitForEvents(t *testing.T, thread *Thread, typ string) []Event {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, event := range thread.Events {
			if event.Type == typ {
				return append([]Event(nil), thread.Events...)
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	b, _ := json.MarshalIndent(thread.Events, "", "  ")
	t.Fatalf("timed out waiting for %s; events: %s", typ, b)
	return nil
}

type fakeClient struct {
	streamResponses []llm.ChatResponse
}

func (f *fakeClient) Chat(ctx context.Context, messages []llm.Message, tools []llm.Tool) (llm.ChatResponse, error) {
	return llm.ChatResponse{}, errors.New("unexpected Chat call")
}

func (f *fakeClient) ChatStream(ctx context.Context, messages []llm.Message, tools []llm.Tool, observe llm.StreamObserver) (llm.ChatResponse, error) {
	if len(f.streamResponses) == 0 {
		return llm.ChatResponse{}, errors.New("unexpected ChatStream call")
	}
	resp := f.streamResponses[0]
	f.streamResponses = f.streamResponses[1:]
	return resp, nil
}

func (f *fakeClient) Provider() string            { return "test" }
func (f *fakeClient) Model() string               { return "test-model" }
func (f *fakeClient) MaxContext() int             { return 128000 }
func (f *fakeClient) SupportsTools() bool         { return true }
func (f *fakeClient) SupportsImageInput() bool    { return false }
func (f *fakeClient) SupportsPromptCaching() bool { return false }
