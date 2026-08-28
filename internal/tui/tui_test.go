package tui

import (
	"context"
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/itwanger/zcoder-go/internal/agent"
)

func TestStripTerminalControlResponses(t *testing.T) {
	cases := map[string]string{
		"]11;rgb:0000/0000/0000\\":              "",
		"\x1b]11;rgb:0000/0000/0000\x1b\\hello": "hello",
		"\x1b[<65;19;29M":                       "",
		"[<65;19;29M<65;19;29M":                 "",
		"hello\x1b[<65;19;29M world":            "hello world",
		"normal text":                           "normal text",
	}
	for input, want := range cases {
		if got := stripTerminalControlResponses(input); got != want {
			t.Fatalf("stripTerminalControlResponses(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestMouseEscapeKeyMsgDoesNotEnterInput(t *testing.T) {
	m := newModel(context.Background(), nil, Startup{})
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("[<65;19;29M")})
	got := updated.(model)
	if got.input.Value() != "" {
		t.Fatalf("mouse escape sequence should not enter input, got %q", got.input.Value())
	}
}

func TestMouseWheelScrollsTranscriptAndDoesNotEnterInput(t *testing.T) {
	m := overflowingTranscriptModel()
	before := m.transcriptView.YOffset

	updated, _ := m.Update(tea.MouseMsg{
		Button: tea.MouseButtonWheelUp,
		Action: tea.MouseActionPress,
		Type:   tea.MouseWheelUp,
	})
	got := updated.(model)
	if got.transcriptView.YOffset >= before {
		t.Fatalf("mouse wheel should scroll transcript upward: before=%d after=%d", before, got.transcriptView.YOffset)
	}
	if got.input.Value() != "" {
		t.Fatalf("mouse wheel should not enter input, got %q", got.input.Value())
	}

	updated, _ = got.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("[[[")})
	got = updated.(model)
	if got.input.Value() != "" {
		t.Fatalf("mouse control fragment should not enter input, got %q", got.input.Value())
	}
}

func TestBracketInputStillWorksWithoutRecentMouseEvent(t *testing.T) {
	m := newModel(context.Background(), nil, Startup{})
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("[")})
	got := updated.(model)
	if got.input.Value() != "[" {
		t.Fatalf("normal bracket input should be preserved, got %q", got.input.Value())
	}
}

func TestCompactTokenCount(t *testing.T) {
	cases := map[int]string{
		999:     "999",
		1200:    "1.2k",
		128000:  "128.0k",
		1200000: "1.2M",
	}
	for input, want := range cases {
		if got := compactTokenCount(input); got != want {
			t.Fatalf("compactTokenCount(%d) = %q, want %q", input, got, want)
		}
	}
}

func TestContextStatusUsesMillionWindowAndGreyEmptyProgress(t *testing.T) {
	m := newModel(context.Background(), nil, Startup{Model: "deepseek-v4-pro"})
	got := m.contextStatus(1200, 1000000)
	plain := ansi.Strip(got)
	if !strings.Contains(plain, "1% 1.2k/1.0M") {
		t.Fatalf("context status should show 1M window, got %q", plain)
	}
	if progressEmptyStyle.GetBackground() != lipgloss.Color("236") {
		t.Fatalf("empty progress area should use grey background")
	}
}

func TestStatusBarStaysSingleLine(t *testing.T) {
	for _, width := range []int{40, 80, 160} {
		m := newModel(context.Background(), nil, Startup{
			Model:      "deepseek-v4-pro",
			CWD:        "/Users/itwanger/Documents/GitHub/zcoder-go",
			MaxContext: 1000000,
		})
		m.width = width
		got := m.statusBar()
		if h := lipgloss.Height(got); h != 1 {
			t.Fatalf("status bar height at width %d = %d, want 1:\n%s", width, h, ansi.Strip(got))
		}
		if w := lipgloss.Width(got); w > width {
			t.Fatalf("status bar width at width %d = %d, want <= %d:\n%s", width, w, width, ansi.Strip(got))
		}
	}
}

func TestTruncateMiddle(t *testing.T) {
	got := truncateMiddle("/Users/itwanger/Documents/GitHub/zcoder-go", 18)
	if got == "/Users/itwanger/Documents/GitHub/zcoder-go" {
		t.Fatal("expected long path to be truncated")
	}
	if len([]rune(got)) > 18 {
		t.Fatalf("truncated path too long: %q", got)
	}
}

func TestInputRows(t *testing.T) {
	if got := inputRows("", 20); got != 1 {
		t.Fatalf("empty input rows = %d, want 1", got)
	}
	if got := inputRows("short", 20); got != 1 {
		t.Fatalf("short input rows = %d, want 1", got)
	}
	if got := inputRows(strings.Repeat("a", 45), 20); got != 3 {
		t.Fatalf("wrapped input rows = %d, want 3", got)
	}
	if got := inputRows(strings.Repeat("a", 200), 20); got != 4 {
		t.Fatalf("long input rows = %d, want cap 4", got)
	}
}

func TestInputBoxUsesFullWidth(t *testing.T) {
	m := newModel(context.Background(), nil, Startup{})
	m.width = 80
	m.updateInputLayout()
	got := m.inputBox()
	if lipglossWidth := printableWidth(got); lipglossWidth != 80 {
		t.Fatalf("input box width = %d, want 80", lipglossWidth)
	}
	if !strings.Contains(got, inputPlaceholder) {
		t.Fatalf("input box should render placeholder %q, got %q", inputPlaceholder, got)
	}
	plain := ansi.Strip(got)
	lines := strings.Split(plain, "\n")
	if len(lines) != inputPaddingTop+1+inputPaddingBottom {
		t.Fatalf("input box rows = %d, want %d: %q", len(lines), inputPaddingTop+1+inputPaddingBottom, plain)
	}
	for i := 0; i < inputPaddingTop; i++ {
		if strings.TrimSpace(lines[i]) != "" {
			t.Fatalf("top input padding row should be blank, got %q", lines[i])
		}
	}
	contentLine := lines[inputPaddingTop]
	if !strings.HasPrefix(contentLine, inputPrompt+inputPlaceholder) {
		t.Fatalf("input should render textarea prompt and placeholder, got %q", contentLine)
	}
	for i := len(lines) - inputPaddingBottom; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) != "" {
			t.Fatalf("bottom input padding row should be blank, got %q", lines[i])
		}
	}

	m.input.SetValue("1")
	m.updateInputLayout()
	got = m.inputBox()
	if lipglossWidth := printableWidth(got); lipglossWidth != 80 {
		t.Fatalf("input box with value width = %d, want 80", lipglossWidth)
	}
	plain = ansi.Strip(got)
	lines = strings.Split(plain, "\n")
	contentLine = lines[inputPaddingTop]
	if !strings.HasPrefix(contentLine, inputPrompt+"1") {
		t.Fatalf("input should render typed value with textarea prompt, got %q", plain)
	}
}

func TestViewKeepsInputInsideTerminalFrame(t *testing.T) {
	m := newModel(context.Background(), nil, Startup{Model: "deepseek-v4-pro"})
	m.width = 80
	m.height = 24
	m.updateInputLayout()
	got := m.View()
	plain := trimLineRight(ansi.Strip(got))
	if h := lipgloss.Height(got); h != m.height {
		t.Fatalf("view height = %d, want %d:\n%s", h, m.height, plain)
	}
	if !strings.Contains(plain, inputPrompt+inputPlaceholder+"\n") {
		t.Fatalf("input should stay in the fixed TUI frame, got:\n%s", plain)
	}
	lines := strings.Split(plain, "\n")
	if last := strings.TrimSpace(lines[len(lines)-1]); !strings.HasPrefix(last, "YOLO DeepSeek V4 Pro") {
		t.Fatalf("status bar should be pinned to the last terminal row, got last line %q:\n%s", last, plain)
	}
	if gap := strings.TrimSpace(lines[len(lines)-2]); gap != "" {
		t.Fatalf("input and status bar should be separated by a blank row, got %q:\n%s", gap, plain)
	}
	if count := strings.Count(plain, "YOLO DeepSeek V4 Pro"); count != 1 {
		t.Fatalf("status bar should render once, got %d occurrences:\n%s", count, plain)
	}
	for _, want := range []string{"Zcoder", "DeepSeek V4 Pro", "What's ready", "Zcoder Go，可以帮你"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("view should contain %q, got:\n%s", want, plain)
		}
	}
}

func TestInputCursorPositionPointsToTextarea(t *testing.T) {
	m := newModel(context.Background(), nil, Startup{Model: "deepseek-v4-pro"})
	m.width = 80
	m.height = 24
	m.input.SetValue("支持Plan-and-Execute吗?")
	m.updateInputLayout()
	m.syncTranscriptViewport(true)

	row, col := m.inputCursorPosition()
	plain := trimLineRight(ansi.Strip(m.View()))
	lines := strings.Split(plain, "\n")
	if row < 1 || row > len(lines) {
		t.Fatalf("cursor row %d outside view height %d:\n%s", row, len(lines), plain)
	}
	if !strings.Contains(lines[row-1], inputPrompt+"支持Plan-and-Execute吗?") {
		t.Fatalf("cursor row should be textarea content row, got row %d %q:\n%s", row, lines[row-1], plain)
	}
	if col <= lipgloss.Width(inputPrompt) {
		t.Fatalf("cursor column should be after input prompt, got %d", col)
	}
	if statusRow := strings.TrimSpace(lines[len(lines)-1]); strings.HasPrefix(lines[row-1], statusRow) {
		t.Fatalf("cursor row should not point to status bar")
	}
}

func TestTranscriptViewportScrollsToPreviousOutput(t *testing.T) {
	m := overflowingTranscriptModel()

	if !m.transcriptView.AtBottom() {
		t.Fatalf("transcript should start at bottom")
	}
	if m.transcriptView.TotalLineCount() <= m.transcriptView.Height {
		t.Fatalf("test setup should overflow viewport: total=%d height=%d", m.transcriptView.TotalLineCount(), m.transcriptView.Height)
	}
	bottomOffset := m.transcriptView.YOffset
	bottomView := ansi.Strip(m.renderTranscriptViewport(m.transcriptView.Height))
	if strings.Contains(bottomView, "line 01") {
		t.Fatalf("bottom view should not show oldest output before scrolling:\n%s", bottomView)
	}

	for !m.transcriptView.AtTop() {
		if !m.handleTranscriptScrollKey(tea.KeyMsg{Type: tea.KeyPgUp}) {
			t.Fatalf("page up should be handled by transcript viewport")
		}
	}
	if m.transcriptView.YOffset >= bottomOffset {
		t.Fatalf("page up should move viewport upward: before=%d after=%d", bottomOffset, m.transcriptView.YOffset)
	}
	scrolledView := ansi.Strip(m.renderTranscriptViewport(m.transcriptView.Height))
	if !strings.Contains(scrolledView, "line 01") {
		t.Fatalf("scrolled view should reveal previous output:\n%s", scrolledView)
	}
}

func TestTranscriptScrollbarAppearsWhenContentOverflows(t *testing.T) {
	m := overflowingTranscriptModel()

	bar := ansi.Strip(strings.Join(m.scrollbar(m.transcriptView), ""))
	if !strings.Contains(bar, "█") || !strings.Contains(bar, "│") {
		t.Fatalf("overflowing transcript should render scrollbar track and thumb, got %q", bar)
	}
}

func overflowingTranscriptModel() model {
	m := newModel(context.Background(), nil, Startup{Model: "deepseek-v4-pro"})
	m.width = 80
	m.height = 20
	m.renderer = nil
	m.entries = nil
	for i := 1; i <= 10; i++ {
		m.entries = append(m.entries, entry{Role: "assistant", Content: fmt.Sprintf("line %02d", i)})
	}
	m.syncTranscriptViewport(true)
	return m
}

func TestDisplayModelName(t *testing.T) {
	cases := map[string]string{
		"deepseek-v4-flash": "DeepSeek V4 Flash",
		"deepseek-v4-pro":   "DeepSeek V4 Pro",
		"glm-5.1":           "GLM 5.1",
	}
	for input, want := range cases {
		if got := displayModelName(input); got != want {
			t.Fatalf("displayModelName(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestFormatEventContent(t *testing.T) {
	thinking := formatEventContent(agent.Event{
		Type:    agent.EventThinking,
		Title:   "Thinking #1",
		Content: "需要先读取文件。",
	})
	if !strings.Contains(thinking, "Thinking #1") || !strings.Contains(thinking, "需要先读取文件。") {
		t.Fatalf("thinking event not formatted correctly: %q", thinking)
	}

	toolUse := formatEventContent(agent.Event{
		Type:    agent.EventToolCall,
		Title:   "read_file",
		Content: `{"path":"README.md"}`,
	})
	if !strings.Contains(toolUse, "Tool use: read_file") || !strings.Contains(toolUse, "README.md") {
		t.Fatalf("tool call event not formatted correctly: %q", toolUse)
	}

	toolResult := formatEventContent(agent.Event{
		Type:    agent.EventToolResult,
		Title:   "read_file",
		Content: "Zcoder Go",
	})
	if !strings.Contains(toolResult, "Tool result: read_file") || !strings.Contains(toolResult, "Zcoder Go") {
		t.Fatalf("tool result event not formatted correctly: %q", toolResult)
	}
}

func TestTranscriptRendersFullProcessAndAnswer(t *testing.T) {
	m := newModel(context.Background(), nil, Startup{})
	m.width = 100
	m.renderer, _ = newMarkdownRenderer(96)
	longAnswer := "## 答案\n\n" + strings.Repeat("- 很长的 Markdown 输出\n", 40)
	m.entries = []entry{
		{Role: "user", Content: "联网搜一下沉默王二是谁"},
		{Role: string(agent.EventThinking), Content: formatEventContent(agent.Event{
			Type:    agent.EventThinking,
			Title:   "Thinking #1",
			Content: "需要先联网搜索。",
		})},
		{Role: string(agent.EventToolCall), Content: formatEventContent(agent.Event{
			Type:    agent.EventToolCall,
			Title:   "web_search",
			Content: `{"query":"沉默王二是谁"}`,
		})},
		{Role: string(agent.EventToolResult), Content: formatEventContent(agent.Event{
			Type:    agent.EventToolResult,
			Title:   "web_search",
			Content: "搜索结果内容。",
		})},
		{Role: "assistant", Content: longAnswer},
	}

	got := m.transcript()
	plain := ansi.Strip(got)
	for _, want := range []string{"Thinking #1", "Tool use: web_search", "沉默王二是谁", "Tool result: web_search", "搜索结果内容", "很长的 Markdown 输出"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("transcript should render %q, got:\n%s", want, got)
		}
	}
	if strings.Contains(plain, "clipped") || strings.Contains(plain, "truncated") {
		t.Fatalf("transcript should not clip or truncate content, got:\n%s", got)
	}
}

func TestApplyStreamEventMergesDeltas(t *testing.T) {
	m := newModel(context.Background(), nil, Startup{})
	m.entries = nil
	first := m.applyStreamEvent(agent.Event{Type: agent.EventThinkingDelta, Title: "Thinking #1", Content: "用户"})
	second := m.applyStreamEvent(agent.Event{Type: agent.EventThinkingDelta, Title: "Thinking #1", Content: "打了个"})
	third := m.applyStreamEvent(agent.Event{Type: agent.EventThinkingDelta, Title: "Thinking #1", Content: "招呼。"})
	if len(m.entries) != 1 {
		t.Fatalf("thinking entries = %d, want 1", len(m.entries))
	}
	if !strings.Contains(m.entries[0].Content, "用户打了个招呼。") {
		t.Fatalf("thinking content not merged: %q", m.entries[0].Content)
	}
	if first != "" || second != "" {
		t.Fatalf("small thinking deltas should be buffered, got first=%q second=%q", first, second)
	}
	plainThird := ansi.Strip(third)
	if !strings.Contains(plainThird, "Thinking #1") || !strings.Contains(plainThird, "用户打了个招呼。") {
		t.Fatalf("thinking stream should flush a complete line, got %q", third)
	}

	firstAnswer := m.applyStreamEvent(agent.Event{Type: agent.EventAnswerDelta, Content: "你"})
	secondAnswer := m.applyStreamEvent(agent.Event{Type: agent.EventAnswerDelta, Content: "好"})
	if len(m.entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(m.entries))
	}
	if m.answerDraft != "你好" {
		t.Fatalf("answer delta draft = %q, want 你好", m.answerDraft)
	}
	if firstAnswer != "" || secondAnswer != "" {
		t.Fatalf("answer deltas should be collected for final markdown render, got %q %q", firstAnswer, secondAnswer)
	}
}

func TestStreamDoneAppendsDraftAsFinalAnswer(t *testing.T) {
	m := newModel(context.Background(), nil, Startup{})
	m.entries = []entry{{Role: "user", Content: "你好"}}
	m.answerDraft = "**你好**"

	updated, _ := m.Update(streamDoneMsg{})
	got := updated.(model)

	if got.answerDraft != "" {
		t.Fatalf("answer draft should reset, got %q", got.answerDraft)
	}
	if len(got.entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(got.entries))
	}
	if got.entries[1].Role != "assistant" || got.entries[1].Content != "**你好**" {
		t.Fatalf("final answer entry = %#v", got.entries[1])
	}
}

func TestToolEventFlushesPendingThinkingLine(t *testing.T) {
	m := newModel(context.Background(), nil, Startup{})
	m.entries = nil
	if got := m.applyStreamEvent(agent.Event{Type: agent.EventThinkingDelta, Title: "Thinking #1", Content: "先查询工具"}); got != "" {
		t.Fatalf("partial thinking should stay buffered, got %q", got)
	}
	got := ansi.Strip(m.applyStreamEvent(agent.Event{
		Type:    agent.EventToolCall,
		Title:   "web_search",
		Content: `{"query":"test"}`,
	}))
	for _, want := range []string{"Thinking #1", "先查询工具", "Tool use: web_search"} {
		if !strings.Contains(got, want) {
			t.Fatalf("tool event should flush pending thinking and render tool use %q, got:\n%s", want, got)
		}
	}
}

func TestAnswerMsgAppendsEventsBeforeAnswer(t *testing.T) {
	m := newModel(context.Background(), nil, Startup{})
	updated, _ := m.Update(answerMsg{
		Answer: "**done**",
		Events: []agent.Event{{
			Type:    agent.EventToolCall,
			Title:   "read_file",
			Content: `{"path":"README.md"}`,
		}},
	})
	got := updated.(model)
	if len(got.entries) != 3 {
		t.Fatalf("entries = %d, want 3", len(got.entries))
	}
	if got.entries[1].Role != string(agent.EventToolCall) {
		t.Fatalf("event role = %q, want %q", got.entries[1].Role, agent.EventToolCall)
	}
	if got.entries[2].Role != "assistant" {
		t.Fatalf("final answer role = %q, want assistant", got.entries[2].Role)
	}
}

func printableWidth(s string) int {
	width := 0
	for _, line := range strings.Split(s, "\n") {
		if w := lipgloss.Width(line); w > width {
			width = w
		}
	}
	return width
}

func trimLineRight(s string) string {
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " ")
	}
	return strings.Join(lines, "\n")
}
