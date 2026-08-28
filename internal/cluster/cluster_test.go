package cluster

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/itwanger/zcoder-go/internal/llm"
)

func TestClusterSimulateAllSucceed(t *testing.T) {
	base := t.TempDir()
	cl := New(Config{
		Agents:       12,
		Concurrency:  4,
		Simulate:     true,
		SimDelay:     5 * time.Millisecond,
		ProjectRoot:  base,
		WorkspaceDir: base,
	}, nil, nil)

	report, err := cl.Run(context.Background(), "write demo modules")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if report.Stats.Total != 12 || report.Stats.Succeeded != 12 || report.Stats.Failed != 0 {
		t.Fatalf("unexpected stats: %+v", report.Stats)
	}
	if report.Stats.PeakConcurrency < 1 || report.Stats.PeakConcurrency > 4 {
		t.Fatalf("peak concurrency %d violates limit 4", report.Stats.PeakConcurrency)
	}
	if len(report.Subtasks) != 12 {
		t.Fatalf("expected 12 subtasks, got %d", len(report.Subtasks))
	}
	if report.Summary == "" {
		t.Fatal("expected a summary from the aggregator")
	}
	// The mock worker wrote its output file into its sandbox.
	file := filepath.Join(report.RunDir, "worker-0001", "output-0001.md")
	if _, err := os.Stat(file); err != nil {
		t.Fatalf("expected sandbox output file %s: %v", file, err)
	}
}

func TestClusterWorkerFailureDoesNotKillCluster(t *testing.T) {
	base := t.TempDir()
	cl := New(Config{
		Agents:       6,
		Concurrency:  3,
		Simulate:     true,
		FailWorker:   3, // 1-based: fails worker-0003 (index 2)
		ProjectRoot:  base,
		WorkspaceDir: base,
	}, nil, nil)

	report, err := cl.Run(context.Background(), "demo task")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if report.Stats.Succeeded != 5 || report.Stats.Failed != 1 {
		t.Fatalf("unexpected stats: %+v", report.Stats)
	}
	if report.Stats.Results[2].Err == nil {
		t.Fatal("expected worker 2 to record an error")
	}
	if report.Summary == "" {
		t.Fatal("expected fallback summary to still be produced")
	}
}

// stubClient lets tests drive the non-simulate path with a fixed client.
type stubClient struct {
	decompose string
	toolCall  bool
}

func (s *stubClient) Chat(ctx context.Context, messages []llm.Message, tools []llm.Tool) (llm.ChatResponse, error) {
	return s.respond(messages), nil
}
func (s *stubClient) ChatStream(ctx context.Context, messages []llm.Message, tools []llm.Tool, observe llm.StreamObserver) (llm.ChatResponse, error) {
	return s.respond(messages), nil
}
func (s *stubClient) Provider() string            { return "stub" }
func (s *stubClient) Model() string               { return "stub-model" }
func (s *stubClient) MaxContext() int             { return 128000 }
func (s *stubClient) SupportsTools() bool         { return true }
func (s *stubClient) SupportsImageInput() bool    { return false }
func (s *stubClient) SupportsPromptCaching() bool { return false }

func (s *stubClient) respond(messages []llm.Message) llm.ChatResponse {
	joined := ""
	if len(messages) > 0 {
		joined = messages[0].Content + "\n" + messages[len(messages)-1].Content
	}
	switch {
	case strings.Contains(joined, decomposeMarker):
		return llm.ChatResponse{Content: s.decompose}
	case strings.Contains(joined, aggregateMarker):
		return llm.ChatResponse{Content: "stub report"}
	case len(messages) > 0 && messages[len(messages)-1].Role == "tool":
		return llm.ChatResponse{Content: "stub worker done"}
	default:
		if s.toolCall {
			return llm.ChatResponse{ToolCalls: []llm.ToolCall{{
				ID:       "call-1",
				Function: llm.FunctionCall{Name: "write_file", Arguments: []byte(`{"path":"a.md","content":"hi"}`)},
			}}}
		}
		return llm.ChatResponse{Content: "stub worker answer"}
	}
}

func TestClusterDecomposeFallbackOnBadJSON(t *testing.T) {
	base := t.TempDir()
	cl := New(Config{
		Agents:       3,
		Concurrency:  2,
		ProjectRoot:  base,
		WorkspaceDir: base,
	}, &stubClient{decompose: "this is not json"}, nil)

	report, err := cl.Run(context.Background(), "demo task")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if report.Stats.Succeeded != 3 {
		t.Fatalf("expected all 3 workers to succeed, got %+v", report.Stats)
	}
	for _, st := range report.Subtasks {
		if !strings.Contains(st.Prompt, "aspect of the following task") {
			t.Fatalf("expected fallback subtask prompt, got %q", st.Prompt)
		}
	}
}

func TestClusterRealClientToolLoop(t *testing.T) {
	base := t.TempDir()
	cl := New(Config{
		Agents:       2,
		Concurrency:  2,
		ProjectRoot:  base,
		WorkspaceDir: base,
	}, &stubClient{toolCall: true}, nil)

	report, err := cl.Run(context.Background(), "demo task")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if report.Stats.Succeeded != 2 {
		t.Fatalf("expected 2 successes, got %+v", report.Stats)
	}
	file := filepath.Join(report.RunDir, "worker-0001", "a.md")
	if _, err := os.Stat(file); err != nil {
		t.Fatalf("expected tool-written file %s: %v", file, err)
	}
}

func TestParseSubtasks(t *testing.T) {
	got := parseSubtasks("```json\n{\"subtasks\":[\"a\",\"b\"]}\n```", 2)
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("unexpected subtasks: %v", got)
	}
	if parseSubtasks("no json here", 2) != nil {
		t.Fatal("expected nil for unparseable content")
	}
}

func TestConfigDefaults(t *testing.T) {
	cl := New(Config{}, nil, nil)
	if cl.cfg.Agents != 1 {
		t.Fatalf("expected agents default 1, got %d", cl.cfg.Agents)
	}
	if cl.cfg.Concurrency != 1 {
		t.Fatalf("expected concurrency clamped to agents, got %d", cl.cfg.Concurrency)
	}
	cl = New(Config{Agents: 10, Concurrency: 99}, nil, nil)
	if cl.cfg.Concurrency != 10 {
		t.Fatalf("expected concurrency clamped to agents, got %d", cl.cfg.Concurrency)
	}
}
