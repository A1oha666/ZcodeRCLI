// Package cluster runs a pool of coding agents concurrently on subtasks of a
// single user task. Demo scope: in-process goroutine pool with a concurrency
// limiter, per-worker sandbox directories, one-shot LLM decomposition and
// aggregation.
package cluster

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/itwanger/zcoder-go/internal/agent"
	"github.com/itwanger/zcoder-go/internal/config"
	"github.com/itwanger/zcoder-go/internal/llm"
	"github.com/itwanger/zcoder-go/internal/tools"
)

const (
	// decomposeMarker and aggregateMarker tag the coordinator LLM calls so
	// clients (including the mock) can tell them apart from worker turns.
	decomposeMarker = "CLUSTER_DECOMPOSE"
	aggregateMarker = "CLUSTER_AGGREGATE"
	// maxAggregatorInputs caps how many worker outputs are fed back to the
	// aggregation call so a 1000-worker run does not blow the context.
	maxAggregatorInputs = 20
	// maxAggregatorOutputChars truncates each worker output fed to the aggregator.
	maxAggregatorOutputChars = 600
)

type Config struct {
	// Agents is the number of workers to spawn (1..5000).
	Agents int
	// Concurrency caps how many workers run at the same time.
	Concurrency int
	// Simulate runs mock agents without any real LLM traffic.
	Simulate bool
	// Simulate-only: 1-based worker number that should fail its first turn; 0 = none fail.
	FailWorker int
	// Isolation selects the per-worker sandbox: "worktree" (default, git
	// worktree per agent, requires a git repo), "dir" (plain subdirectory),
	// or "" (auto: dir for simulate, worktree when the project is a git repo).
	Isolation string
	// KeepWorktrees skips worktree cleanup after the run so changes can be
	// inspected or committed manually.
	KeepWorktrees bool
	// SimDelay is the base per-call latency injected by the mock client.
	SimDelay time.Duration
	// ProjectRoot is the workspace the cluster operates on.
	ProjectRoot string
	// WorkspaceDir overrides the sandbox base directory (default <root>/.zcoder/cluster).
	WorkspaceDir string
}

type Event struct {
	Worker  int
	Type    string // "plan", "start", "done", "error", "info"
	Content string
}

type Observer func(Event)

type SubTask struct {
	Index  int
	Prompt string
}

type WorkerResult struct {
	Index    int
	Prompt   string
	Output   string
	Duration time.Duration
	Err      error
}

type Stats struct {
	Total           int
	Succeeded       int
	Failed          int
	Duration        time.Duration
	PeakConcurrency int
	Results         []WorkerResult
}

type Report struct {
	Task     string
	Subtasks []SubTask
	Summary  string
	Stats    Stats
	RunDir   string
}

type Cluster struct {
	cfg     Config
	client  llm.Client
	observe Observer
	baseCfg config.Config
}

// New builds a cluster. In simulate mode the client is only used as a template;
// each worker gets its own mock client.
func New(cfg Config, client llm.Client, observe Observer) *Cluster {
	if cfg.Agents < 1 {
		cfg.Agents = 1
	}
	if cfg.Agents > 5000 {
		cfg.Agents = 5000
	}
	if cfg.Concurrency < 1 {
		cfg.Concurrency = 8
	}
	if cfg.Concurrency > cfg.Agents {
		cfg.Concurrency = cfg.Agents
	}
	if cfg.ProjectRoot == "" {
		cfg.ProjectRoot = "."
	}
	return &Cluster{cfg: cfg, client: client, observe: observe}
}

func (c *Cluster) emit(ev Event) {
	if c.observe != nil {
		c.observe(ev)
	}
}

// clientFor picks the LLM client for a worker. Index -1 is the coordinator.
func (c *Cluster) clientFor(index int) llm.Client {
	if !c.cfg.Simulate {
		return c.client
	}
	if index < 0 {
		return NewMockClient(MockConfig{Worker: -1, Subtasks: c.cfg.Agents, Delay: c.cfg.SimDelay})
	}
	return NewMockClient(MockConfig{Worker: index, Delay: c.cfg.SimDelay, FailWorker: c.cfg.FailWorker - 1})
}

func (c *Cluster) Run(ctx context.Context, task string) (Report, error) {
	task = strings.TrimSpace(task)
	if task == "" {
		return Report{}, fmt.Errorf("cluster task is empty")
	}
	started := time.Now()
	c.baseCfg = config.Load()

	sb, err := c.provision()
	if err != nil {
		return Report{}, err
	}
	defer sb.cleanup(c)

	subtasks, err := c.decompose(ctx, task)
	if err != nil {
		return Report{}, err
	}
	for _, st := range subtasks {
		c.emit(Event{Worker: st.Index, Type: "plan", Content: st.Prompt})
	}

	stats := c.runWorkers(ctx, subtasks, sb)

	summary, aggErr := c.aggregate(ctx, task, stats)
	if aggErr != nil {
		c.emit(Event{Type: "info", Content: "aggregation failed: " + aggErr.Error()})
		summary = fallbackSummary(task, stats)
	}

	stats.Duration = time.Since(started)
	return Report{
		Task:     task,
		Subtasks: subtasks,
		Summary:  summary,
		Stats:    stats,
		RunDir:   sb.runDir,
	}, nil
}

// sandboxes describes where the workers of one run operate.
type sandboxes struct {
	runDir   string
	worktree bool
	pool     *worktreePool
}

// cleanup tears down worktrees after the run unless the caller asked to keep
// them for manual inspection.
func (s *sandboxes) cleanup(c *Cluster) {
	if s.pool == nil {
		return
	}
	if c.cfg.KeepWorktrees {
		c.emit(Event{Type: "info", Content: "keeping worktrees under " + s.runDir})
		return
	}
	s.pool.removeAll()
}

// resolveIsolation decides whether this run uses git worktree sandboxes.
func (c *Cluster) resolveIsolation() bool {
	switch strings.ToLower(strings.TrimSpace(c.cfg.Isolation)) {
	case "dir":
		return false
	case "worktree":
		if !IsGitRepo(c.cfg.ProjectRoot) {
			c.emit(Event{Type: "info", Content: "project root is not a git repo; falling back to dir sandboxes"})
			return false
		}
		return true
	default: // auto
		if c.cfg.Simulate {
			// Simulated workers only write one mock file each; creating
			// thousands of worktree checkouts would be pure overhead.
			return false
		}
		return IsGitRepo(c.cfg.ProjectRoot)
	}
}

// provision prepares the per-worker sandboxes for one run.
func (c *Cluster) provision() (*sandboxes, error) {
	isolate := c.resolveIsolation()
	base := sandboxBaseFor(isolate, c.cfg.ProjectRoot, c.cfg.WorkspaceDir)
	runDir := filepath.Join(base, time.Now().Format("20060102-150405"))

	if isolate {
		if err := os.MkdirAll(runDir, 0o755); err != nil {
			return nil, fmt.Errorf("create run dir %s: %w", runDir, err)
		}
		pool := newWorktreePool(c.cfg.ProjectRoot)
		for i := 0; i < c.cfg.Agents; i++ {
			path := workerDir(runDir, i)
			if err := pool.create(path); err != nil {
				pool.removeAll()
				return nil, fmt.Errorf("create worker worktree: %w", err)
			}
		}
		c.emit(Event{Type: "info", Content: fmt.Sprintf("isolated %d workers in git worktrees under %s", c.cfg.Agents, runDir)})
		return &sandboxes{runDir: runDir, worktree: true, pool: pool}, nil
	}

	for i := 0; i < c.cfg.Agents; i++ {
		dir := workerDir(runDir, i)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create worker sandbox %s: %w", dir, err)
		}
	}
	return &sandboxes{runDir: runDir}, nil
}

func workerDir(runDir string, index int) string {
	return filepath.Join(runDir, fmt.Sprintf("worker-%04d", index+1))
}

// decompose turns the task into N subtask prompts with one LLM call, falling
// back to perspective-based splitting when parsing fails.
func (c *Cluster) decompose(ctx context.Context, task string) ([]SubTask, error) {
	prompt := fmt.Sprintf(
		"%s\nSplit the following task into exactly %d independent, self-contained subtasks "+
			"that different coding agents can work on in parallel. "+
			"Reply with JSON only: {\"subtasks\": [\"...\", ...]}.\n\nTask:\n%s",
		decomposeMarker, c.cfg.Agents, task)
	resp, err := c.callLLM(ctx, -1, []llm.Message{llm.User(prompt)}, nil)
	if err != nil {
		return nil, fmt.Errorf("decompose task: %w", err)
	}
	subtasks := parseSubtasks(resp.Content, c.cfg.Agents)
	if len(subtasks) == 0 {
		subtasks = fallbackSubtasks(task, c.cfg.Agents)
	}
	out := make([]SubTask, c.cfg.Agents)
	for i := 0; i < c.cfg.Agents; i++ {
		out[i] = SubTask{Index: i, Prompt: subtasks[i%len(subtasks)]}
	}
	return out, nil
}

type subtaskPayload struct {
	Subtasks []string `json:"subtasks"`
}

func parseSubtasks(content string, want int) []string {
	content = strings.TrimSpace(content)
	// Tolerate markdown fences around the JSON payload.
	content = strings.TrimPrefix(content, "```json")
	content = strings.TrimPrefix(content, "```")
	content = strings.TrimSuffix(content, "```")
	var payload subtaskPayload
	if err := json.Unmarshal([]byte(strings.TrimSpace(content)), &payload); err != nil {
		return nil
	}
	out := make([]string, 0, len(payload.Subtasks))
	for _, s := range payload.Subtasks {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func fallbackSubtasks(task string, n int) []string {
	perspectives := []string{
		"implementation", "testing", "documentation", "refactoring",
		"edge cases", "performance", "error handling", "observability",
	}
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		p := perspectives[i%len(perspectives)]
		out = append(out, fmt.Sprintf("Work on the %s aspect of the following task:\n%s", p, task))
	}
	return out
}

func (c *Cluster) runWorkers(ctx context.Context, subtasks []SubTask, sb *sandboxes) Stats {
	stats := Stats{Total: len(subtasks), Results: make([]WorkerResult, len(subtasks))}
	var (
		wg        sync.WaitGroup
		sem       = make(chan struct{}, c.cfg.Concurrency)
		inflight  atomic.Int64
		peak      atomic.Int64
		succeeded atomic.Int64
	)

	for _, st := range subtasks {
		st := st
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			cur := inflight.Add(1)
			for {
				old := peak.Load()
				if cur <= old || peak.CompareAndSwap(old, cur) {
					break
				}
			}
			defer inflight.Add(-1)

			c.emit(Event{Worker: st.Index, Type: "start"})
			result := c.runWorker(ctx, st, sb)
			stats.Results[st.Index] = result
			if result.Err != nil {
				c.emit(Event{Worker: st.Index, Type: "error", Content: result.Err.Error()})
				return // demo policy: one failed agent must not kill the cluster
			}
			succeeded.Add(1)
			c.emit(Event{Worker: st.Index, Type: "done", Content: fmt.Sprintf("done in %s", result.Duration.Round(time.Millisecond))})
		}()
	}
	wg.Wait()
	stats.Succeeded = int(succeeded.Load())
	stats.Failed = stats.Total - stats.Succeeded
	stats.PeakConcurrency = int(peak.Load())
	return stats
}

func (c *Cluster) runWorker(ctx context.Context, st SubTask, sb *sandboxes) (result WorkerResult) {
	result = WorkerResult{Index: st.Index, Prompt: st.Prompt}
	started := time.Now()
	defer func() { result.Duration = time.Since(started) }()

	registry := tools.NewRegistry(workerDir(sb.runDir, st.Index), tools.Options{Config: c.baseCfg})
	defer registry.Close()

	worker := agent.New(c.clientFor(st.Index), registry, nil, nil)
	output, err := worker.Run(ctx, st.Prompt)
	if err != nil {
		result.Err = err
		return result
	}
	result.Output = output
	return result
}

// aggregate synthesizes a final report from worker outputs with one LLM call.
func (c *Cluster) aggregate(ctx context.Context, task string, stats Stats) (string, error) {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("Task: %s\nWorkers: %d total, %d succeeded, %d failed.\n\n", task, stats.Total, stats.Succeeded, stats.Failed))
	shown := 0
	for _, r := range stats.Results {
		if shown >= maxAggregatorInputs {
			break
		}
		if r.Err != nil || strings.TrimSpace(r.Output) == "" {
			continue
		}
		output := r.Output
		if len(output) > maxAggregatorOutputChars {
			output = output[:maxAggregatorOutputChars] + "…"
		}
		b.WriteString(fmt.Sprintf("## worker-%04d\n%s\n\n", r.Index+1, output))
		shown++
	}
	if shown == 0 {
		b.WriteString("(no worker output available)\n")
	}
	prompt := fmt.Sprintf(
		"%s\nMerge the following worker reports into one concise final report for the user. "+
			"Mention aggregate progress when only a sample of worker outputs is included.\n\n%s",
		aggregateMarker, b.String())
	resp, err := c.callLLM(ctx, -1, []llm.Message{llm.User(prompt)}, nil)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(resp.Content), nil
}

func (c *Cluster) callLLM(ctx context.Context, workerIndex int, messages []llm.Message, toolDefs []llm.Tool) (llm.ChatResponse, error) {
	return c.clientFor(workerIndex).Chat(ctx, messages, toolDefs)
}

func fallbackSummary(task string, stats Stats) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("Cluster run of %d agents on %q finished: %d succeeded, %d failed in %s.\n",
		stats.Total, task, stats.Succeeded, stats.Failed, stats.Duration.Round(time.Millisecond)))
	for _, r := range stats.Results {
		if r.Err != nil {
			b.WriteString(fmt.Sprintf("- worker-%04d failed: %v\n", r.Index+1, r.Err))
		}
	}
	return b.String()
}
