package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/itwanger/zcoder-go/internal/llm"
	"github.com/itwanger/zcoder-go/internal/skill"
	"github.com/itwanger/zcoder-go/internal/snapshot"
	"github.com/itwanger/zcoder-go/internal/tools"
)

type Agent struct {
	client    llm.Client
	tools     *tools.Registry
	memory    *MemoryStore
	skills    *skill.Registry
	snapshots *snapshot.Service
	history   []llm.Message
}

type EventType string

const (
	EventThinking      EventType = "thinking"
	EventThinkingDelta EventType = "thinking_delta"
	EventAnswerDelta   EventType = "answer_delta"
	EventToolCall      EventType = "tool_call"
	EventToolResult    EventType = "tool_result"
)

type Event struct {
	Type    EventType
	Title   string
	Content string
}

type Observer func(Event)

type RunMode string

const (
	RunModeReact RunMode = "react"
	RunModePlan  RunMode = "plan"
	RunModeTeam  RunMode = "team"
)

func New(client llm.Client, registry *tools.Registry, memory *MemoryStore, skills *skill.Registry) *Agent {
	a := &Agent{
		client:  client,
		tools:   registry,
		memory:  memory,
		skills:  skills,
		history: []llm.Message{llm.System("")},
	}
	a.refreshSystemPrompt("")
	return a
}

func (a *Agent) Clone() *Agent {
	cp := *a
	cp.history = append([]llm.Message(nil), a.history...)
	return &cp
}

func (a *Agent) SetSnapshotService(s *snapshot.Service) {
	a.snapshots = s
}

func (a *Agent) Run(ctx context.Context, input string) (string, error) {
	return a.RunWithObserver(ctx, input, nil)
}

func (a *Agent) RunCommand(ctx context.Context, input string) (string, error) {
	return a.RunCommandWithObserver(ctx, input, nil)
}

func (a *Agent) RunCommandWithObserver(ctx context.Context, input string, observe Observer) (string, error) {
	mode, prompt := ParseRunCommand(input)
	return a.RunModeWithObserver(ctx, mode, prompt, observe)
}

func (a *Agent) RunMode(ctx context.Context, mode RunMode, input string) (string, error) {
	return a.RunModeWithObserver(ctx, mode, input, nil)
}

func (a *Agent) RunModeWithObserver(ctx context.Context, mode RunMode, input string, observe Observer) (string, error) {
	switch NormalizeRunMode(mode) {
	case "", RunModeReact:
		return a.RunWithObserver(ctx, input, observe)
	case RunModePlan:
		return a.PlanAndExecuteWithObserver(ctx, input, observe)
	case RunModeTeam:
		return a.Team(ctx, input)
	default:
		return "", fmt.Errorf("unknown run mode %q", mode)
	}
}

func ParseRunCommand(input string) (RunMode, string) {
	text := strings.TrimSpace(input)
	switch {
	case strings.HasPrefix(text, "/plan "):
		return RunModePlan, strings.TrimSpace(strings.TrimPrefix(text, "/plan "))
	case strings.EqualFold(text, "/plan"):
		return RunModePlan, ""
	case strings.HasPrefix(text, "/team "):
		return RunModeTeam, strings.TrimSpace(strings.TrimPrefix(text, "/team "))
	case strings.EqualFold(text, "/team"):
		return RunModeTeam, ""
	default:
		return RunModeReact, input
	}
}

func NormalizeRunMode(mode RunMode) RunMode {
	switch strings.ToLower(strings.TrimSpace(string(mode))) {
	case "", string(RunModeReact):
		return RunModeReact
	case string(RunModePlan), "plan-and-execute", "plan_and_execute":
		return RunModePlan
	case string(RunModeTeam), "multi-agent", "multi_agent":
		return RunModeTeam
	default:
		return mode
	}
}

func IsRunMode(mode RunMode) bool {
	switch NormalizeRunMode(mode) {
	case RunModeReact, RunModePlan, RunModeTeam:
		return true
	default:
		return false
	}
}

func (a *Agent) RunWithObserver(ctx context.Context, input string, observe Observer) (string, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return "", nil
	}
	if a.snapshots != nil {
		_, _ = a.snapshots.Create("pre-turn")
		defer func() { _, _ = a.snapshots.Create("post-turn") }()
	}
	a.refreshSystemPrompt(input)
	a.history = append(a.history, a.userMessage(input))

	var final string
	seenToolCalls := map[string]int{}
	for iter := 0; iter < 10; iter++ {
		resp, err := a.streamChat(ctx, a.history, a.tools.Definitions(), observe, fmt.Sprintf("Thinking #%d", iter+1), true)
		if err != nil {
			return "", err
		}
		if len(resp.ToolCalls) == 0 {
			final = strings.TrimSpace(resp.Content)
			a.history = append(a.history, llm.Assistant(final))
			return final, nil
		}
		if repeatedToolCall(resp.ToolCalls, seenToolCalls) {
			emit(observe, Event{
				Type:    EventToolResult,
				Title:   "loop_guard",
				Content: "Stopped repeated tool calls and asked the model to produce a final answer from the available context.",
			})
			return a.finalizeWithoutTools(ctx, observe, "The previous assistant response repeated a tool call that has already been attempted. Stop calling tools. Produce the best final answer from the available context and explicitly mention any unavailable search/configuration if it limits the answer.")
		}
		for _, call := range resp.ToolCalls {
			emit(observe, Event{
				Type:    EventToolCall,
				Title:   call.Function.Name,
				Content: formatToolCall(call),
			})
		}
		a.history = append(a.history, llm.AssistantWithTools(resp.Content, resp.ToolCalls))
		results := a.tools.ExecuteAll(ctx, resp.ToolCalls)
		for _, result := range results {
			emit(observe, Event{
				Type:    EventToolResult,
				Title:   result.Name,
				Content: result.Content,
			})
			a.history = append(a.history, llm.ToolResult(result.ID, result.Name, result.Content))
		}
	}
	emit(observe, Event{
		Type:    EventToolResult,
		Title:   "loop_guard",
		Content: "The ReAct loop reached the iteration limit; switching to a final no-tool response.",
	})
	return a.finalizeWithoutTools(ctx, observe, "The ReAct loop reached its iteration limit. Stop calling tools and produce the best final answer from the available context. If the available tool results are insufficient, say exactly what is missing instead of retrying tools.")
}

func (a *Agent) PlanAndExecute(ctx context.Context, input string) (string, error) {
	return a.PlanAndExecuteWithObserver(ctx, input, nil)
}

func (a *Agent) PlanAndExecuteWithObserver(ctx context.Context, input string, observe Observer) (string, error) {
	plannerPrompt := "Create a concise execution plan for the user request. Return numbered tasks with dependencies when useful."
	planResp, err := a.streamChat(ctx, []llm.Message{llm.System(a.systemPrompt("")), llm.User(plannerPrompt + "\n\nUser request:\n" + input)}, nil, observe, "Planning", false)
	if err != nil {
		return "", err
	}
	plan := strings.TrimSpace(planResp.Content)
	if plan == "" {
		plan = "1. Execute the request directly with available tools."
	}
	answer, err := a.RunWithObserver(ctx, "Plan approved. Execute this request using the following plan:\n\n"+plan+"\n\nOriginal request:\n"+input, observe)
	if err != nil {
		return "", err
	}
	return "Plan:\n" + plan + "\n\nResult:\n" + answer, nil
}

func (a *Agent) Team(ctx context.Context, input string) (string, error) {
	roles := []string{"Planner", "Worker", "Reviewer"}
	outputs := make([]string, 0, len(roles))
	contextText := input
	for _, role := range roles {
		resp, err := a.client.Chat(ctx, []llm.Message{
			llm.System(a.systemPrompt("") + "\n\nYou are the " + role + " in a Zcoder multi-agent workflow."),
			llm.User(contextText),
		}, a.tools.Definitions())
		if err != nil {
			return "", err
		}
		outputs = append(outputs, "## "+role+"\n"+strings.TrimSpace(resp.Content))
		contextText += "\n\n" + role + " output:\n" + resp.Content
	}
	return strings.Join(outputs, "\n\n"), nil
}

func (a *Agent) refreshSystemPrompt(query string) {
	if len(a.history) == 0 {
		a.history = append(a.history, llm.System(""))
	}
	a.history[0] = llm.System(a.systemPrompt(query))
}

func (a *Agent) systemPrompt(query string) string {
	var b strings.Builder
	b.WriteString("You are Zcoder Go, a terminal-first coding agent. Work with the real local workspace. ")
	b.WriteString("Prefer deterministic tools for repository facts, use grep/read before broad semantic search, and never invent tool results.\n\n")
	b.WriteString("## Current Runtime\n")
	b.WriteString("Date: " + time.Now().Format("2006-01-02") + "\n")
	b.WriteString("Provider: " + a.client.Provider() + "\n")
	b.WriteString("Model: " + a.client.Model() + "\n\n")
	if a.skills != nil {
		b.WriteString(a.skills.IndexForPrompt(4000))
		b.WriteString("\n")
	}
	if a.memory != nil {
		entries := a.memory.Relevant(query, 8)
		if len(entries) > 0 {
			b.WriteString("## Long Term Memory\n")
			for _, e := range entries {
				b.WriteString("- " + e.Content + "\n")
			}
			b.WriteString("\n")
		}
	}
	b.WriteString("## Tool Policy\n")
	b.WriteString("Use tools when they can verify local files, run tests, search code, fetch web pages, call MCP servers, or save explicit memory. ")
	b.WriteString("When the user explicitly asks to search the web, look something up online, 联网搜索, 搜一下, or 查询最新信息, call the web_search tool before answering. ")
	b.WriteString("Dangerous operations are path-guarded, command-guarded and audited.\n")
	return b.String()
}

func (a *Agent) userMessage(input string) llm.Message {
	var b strings.Builder
	if a.skills != nil {
		for _, loaded := range a.skills.ConsumeLoadedBodies() {
			b.WriteString("## Loaded Skill: " + loaded.Name + "\n")
			b.WriteString(loaded.Body)
			b.WriteString("\n\n---\n")
		}
	}
	b.WriteString(input)
	return parseImageRefs(b.String())
}

func parseImageRefs(text string) llm.Message {
	// Keep the data model ready for multimodal providers. The first baseline keeps
	// local image payloads as explicit text references to avoid silently sending
	// old screenshots or unrelated editor state.
	if !strings.Contains(text, "@image:") {
		return llm.User(text)
	}
	parts := []llm.ContentPart{{Type: "text", Text: text}}
	return llm.Message{Role: "user", Content: text, Parts: parts}
}

func FormatToolJSON(v any) string {
	b, _ := json.MarshalIndent(v, "", "  ")
	return string(b)
}

func emit(observe Observer, event Event) {
	if observe != nil {
		observe(event)
	}
}

func (a *Agent) streamChat(ctx context.Context, messages []llm.Message, toolDefs []llm.Tool, observe Observer, thinkingTitle string, streamContent bool) (llm.ChatResponse, error) {
	return a.client.ChatStream(ctx, messages, toolDefs, func(event llm.StreamEvent) {
		switch event.Type {
		case llm.StreamReasoningDelta:
			emit(observe, Event{
				Type:    EventThinkingDelta,
				Title:   thinkingTitle,
				Content: event.Delta,
			})
		case llm.StreamContentDelta:
			if streamContent {
				emit(observe, Event{
					Type:    EventAnswerDelta,
					Title:   "Assistant",
					Content: event.Delta,
				})
			}
		}
	})
}

func (a *Agent) finalizeWithoutTools(ctx context.Context, observe Observer, instruction string) (string, error) {
	messages := append([]llm.Message(nil), a.history...)
	messages = append(messages, llm.User(instruction))
	resp, err := a.streamChat(ctx, messages, nil, observe, "Finalizing", true)
	if err != nil {
		return "", err
	}
	final := strings.TrimSpace(resp.Content)
	if final == "" {
		final = "I could not produce a final answer from the available context."
	}
	a.history = append(a.history, llm.Assistant(final))
	return final, nil
}

func repeatedToolCall(calls []llm.ToolCall, seen map[string]int) bool {
	repeated := false
	for _, call := range calls {
		key := toolCallSignature(call)
		seen[key]++
		if seen[key] > 1 {
			repeated = true
		}
	}
	return repeated
}

func toolCallSignature(call llm.ToolCall) string {
	return call.Function.Name + ":" + strings.TrimSpace(string(call.Function.Arguments))
}

func formatToolCall(call llm.ToolCall) string {
	args := strings.TrimSpace(string(call.Function.Arguments))
	if args == "" {
		args = "{}"
	}
	var decoded any
	if json.Unmarshal(call.Function.Arguments, &decoded) == nil {
		args = FormatToolJSON(decoded)
	}
	return args
}
