package cluster

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"strings"
	"sync/atomic"
	"time"

	"github.com/itwanger/zcoder-go/internal/llm"
)

// MockConfig controls the simulated LLM behaviour.
type MockConfig struct {
	// Worker is the agent index this client serves; -1 means coordinator.
	Worker int
	// Subtasks is the number of subtasks the coordinator should produce.
	Subtasks int
	// Delay is the base per-call latency; a small random jitter is added.
	Delay time.Duration
	// FailWorker is the 0-based worker index that fails its first turn; < 0 disables it.
	FailWorker int
}

// MockClient is a stateless llm.Client that simulates a coding agent: the
// coordinator returns subtask JSON, workers emit one write_file tool call and
// then a final answer, and the aggregator returns a merged report. It lets the
// whole cluster pipeline be exercised without any API key.
type MockClient struct {
	cfg    MockConfig
	calls  atomic.Int64
	marked atomic.Bool
}

func NewMockClient(cfg MockConfig) *MockClient {
	if cfg.Subtasks <= 0 {
		cfg.Subtasks = 1
	}
	if cfg.FailWorker == 0 {
		cfg.FailWorker = -1
	}
	return &MockClient{cfg: cfg}
}

func (m *MockClient) Provider() string { return "mock" }
func (m *MockClient) Model() string    { return "mock-agent" }
func (m *MockClient) MaxContext() int  { return 128000 }

func (m *MockClient) SupportsTools() bool         { return true }
func (m *MockClient) SupportsImageInput() bool    { return false }
func (m *MockClient) SupportsPromptCaching() bool { return false }

func (m *MockClient) Chat(ctx context.Context, messages []llm.Message, tools []llm.Tool) (llm.ChatResponse, error) {
	return m.respond(ctx, messages)
}

func (m *MockClient) ChatStream(ctx context.Context, messages []llm.Message, tools []llm.Tool, observe llm.StreamObserver) (llm.ChatResponse, error) {
	resp, err := m.respond(ctx, messages)
	if err != nil {
		return resp, err
	}
	if observe != nil && resp.Content != "" {
		observe(llm.StreamEvent{Type: llm.StreamContentDelta, Delta: resp.Content})
	}
	return resp, nil
}

func (m *MockClient) respond(ctx context.Context, messages []llm.Message) (llm.ChatResponse, error) {
	if err := m.simulateLatency(ctx); err != nil {
		return llm.ChatResponse{}, err
	}
	last := llm.Message{}
	if len(messages) > 0 {
		last = messages[len(messages)-1]
	}
	joined := lastMessageText(messages)

	switch {
	case strings.Contains(joined, decomposeMarker):
		subtasks := make([]string, 0, m.cfg.Subtasks)
		for i := 0; i < m.cfg.Subtasks; i++ {
			subtasks = append(subtasks, fmt.Sprintf("mock subtask %d: scaffold one module", i+1))
		}
		payload, _ := json.Marshal(map[string]any{"subtasks": subtasks})
		return llm.ChatResponse{Content: string(payload)}, nil
	case strings.Contains(joined, aggregateMarker):
		return llm.ChatResponse{Content: "Mock cluster report: all simulated worker outputs merged."}, nil
	case last.Role == "tool":
		return llm.ChatResponse{
			Content: fmt.Sprintf("worker-%04d finished: wrote output-%04d.md in its sandbox", m.cfg.Worker+1, m.cfg.Worker+1),
		}, nil
	case m.cfg.FailWorker >= 0 && m.cfg.FailWorker == m.cfg.Worker && !m.marked.Swap(true):
		return llm.ChatResponse{}, fmt.Errorf("simulated provider failure for worker-%04d", m.cfg.Worker+1)
	default:
		args, _ := json.Marshal(map[string]string{
			"path":    fmt.Sprintf("output-%04d.md", m.cfg.Worker+1),
			"content": fmt.Sprintf("# output of worker-%04d\n\nsimulated coding result\n", m.cfg.Worker+1),
		})
		return llm.ChatResponse{
			ToolCalls: []llm.ToolCall{{
				ID:       fmt.Sprintf("mock-call-%04d-%d", m.cfg.Worker+1, m.calls.Load()),
				Function: llm.FunctionCall{Name: "write_file", Arguments: args},
			}},
		}, nil
	}
}

func (m *MockClient) simulateLatency(ctx context.Context) error {
	m.calls.Add(1)
	delay := m.cfg.Delay
	if delay > 0 {
		delay += time.Duration(rand.Int63n(int64(delay / 2)))
	}
	if delay <= 0 {
		return nil
	}
	select {
	case <-time.After(delay):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func lastMessageText(messages []llm.Message) string {
	var b strings.Builder
	// Only scan the first (system) and last message to keep this cheap.
	if len(messages) > 0 {
		b.WriteString(messages[0].Content)
		b.WriteString("\n")
		b.WriteString(messages[len(messages)-1].Content)
	}
	return b.String()
}
