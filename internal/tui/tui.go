package tui

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
	xansi "github.com/charmbracelet/x/ansi"

	"github.com/itwanger/zcoder-go/internal/agent"
)

type Startup struct {
	Version       string
	Provider      string
	Model         string
	CWD           string
	SkillsEnabled int
	SkillsTotal   int
	MCPReady      int
	MCPTools      int
	MaxContext    int
}

type model struct {
	ctx            context.Context
	agent          *agent.Agent
	startup        Startup
	input          textarea.Model
	transcriptView viewport.Model
	lastMouseEvent time.Time
	cursorStop     <-chan struct{}
	width          int
	height         int
	entries        []entry
	running        bool
	mode           string
	status         string
	err            error
	renderer       *glamour.TermRenderer
	stream         <-chan tea.Msg
	answerDraft    string
	thinkingTitle  string
	thinkingLine   string
	thinkingHeader bool
}

type entry struct {
	Role    string
	Content string
	Time    time.Time
}

type answerMsg struct {
	Prompt string
	Answer string
	Events []agent.Event
	Err    error
}

type streamEventMsg struct {
	Event agent.Event
}

type streamDoneMsg struct {
	Answer string
	Err    error
}

type streamClosedMsg struct{}

func Run(ctx context.Context, ag *agent.Agent, startup Startup) error {
	cursorStop := make(chan struct{})
	m := newModel(ctx, ag, startup)
	m.cursorStop = cursorStop
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion(), tea.WithOutput(os.Stdout))
	_, err := p.Run()
	close(cursorStop)
	return err
}

func newModel(ctx context.Context, ag *agent.Agent, startup Startup) model {
	input := textarea.New()
	input.Placeholder = inputPlaceholder
	input.Prompt = inputPrompt
	input.ShowLineNumbers = false
	input.EndOfBufferCharacter = ' '
	input.CharLimit = 20000
	input.MaxHeight = maxInputRows
	input.MaxWidth = 0
	input.FocusedStyle.Base = inputFillStyle
	input.FocusedStyle.CursorLine = inputFillStyle
	input.FocusedStyle.Prompt = inputPromptStyle
	input.FocusedStyle.Placeholder = inputPlaceholderStyle
	input.FocusedStyle.Text = inputTextStyle
	input.FocusedStyle.EndOfBuffer = inputFillStyle
	input.SetWidth(80)
	input.SetHeight(1)
	input.Focus()
	transcriptView := viewport.New(79, 20)
	transcriptView.MouseWheelEnabled = true
	transcriptView.MouseWheelDelta = 3
	renderer, _ := newMarkdownRenderer(100)
	m := model{
		ctx:            ctx,
		agent:          ag,
		startup:        startup,
		input:          input,
		transcriptView: transcriptView,
		width:          100,
		height:         30,
		mode:           "YOLO",
		status:         "idle",
		renderer:       renderer,
		entries: []entry{{
			Role:    "assistant",
			Content: "你好！我是 Zcoder Go，可以帮你处理代码、工具调用、搜索、MCP、Skill、RAG 和多 Agent 任务。",
			Time:    time.Now(),
		}},
	}
	m.syncTranscriptViewport(true)
	return m
}

func (m model) Init() tea.Cmd {
	return tea.Batch(tea.HideCursor, m.input.Focus())
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.updateInputLayout()
		if m.renderer != nil {
			m.renderer, _ = newMarkdownRenderer(max(40, msg.Width-scrollbarWidth-4))
		}
		m.syncTranscriptViewport(m.transcriptView.AtBottom())
	case tea.KeyMsg:
		if m.isRecentMouseControlFragment(msg) {
			m.lastMouseEvent = time.Now()
			return m, nil
		}
		if isTerminalControlResponse(msg) {
			return m, nil
		}
		if handled := m.handleTranscriptScrollKey(msg); handled {
			return m, m.placeTerminalCursor()
		}
		switch msg.String() {
		case "ctrl+c", "esc":
			if m.running {
				m.status = "cancel requested"
				return m, nil
			}
			return m, tea.Quit
		case "ctrl+d":
			return m, tea.Quit
		case "enter":
			if m.running {
				return m, nil
			}
			text := strings.TrimSpace(m.input.Value())
			if text == "" {
				return m, nil
			}
			if text == "/exit" || text == "/quit" {
				return m, tea.Quit
			}
			m.input.Reset()
			userEntry := entry{Role: "user", Content: text, Time: time.Now()}
			m.entries = append(m.entries, userEntry)
			m.running = true
			m.status = "running"
			m.answerDraft = ""
			m.resetThinkingBuffer()
			m.syncTranscriptViewport(true)
			stream := make(chan tea.Msg, 256)
			m.stream = stream
			return m, tea.Batch(m.runPrompt(text, stream), waitForStream(stream), m.placeTerminalCursor())
		}
	case tea.MouseMsg:
		m.lastMouseEvent = time.Now()
		m.transcriptView, cmd = m.transcriptView.Update(msg)
		return m, tea.Batch(cmd, m.placeTerminalCursor())
	case streamEventMsg:
		wasAtBottom := m.transcriptView.AtBottom()
		m.applyStreamEvent(msg.Event)
		m.syncTranscriptViewport(wasAtBottom)
		return m, waitForStream(m.stream)
	case streamDoneMsg:
		wasAtBottom := m.transcriptView.AtBottom()
		m.running = false
		m.status = "idle"
		m.stream = nil
		m.resetThinkingBuffer()
		answer := msg.Answer
		if strings.TrimSpace(answer) == "" {
			answer = m.answerDraft
		}
		if msg.Err != nil {
			errorEntry := entry{Role: "error", Content: msg.Err.Error(), Time: time.Now()}
			m.entries = append(m.entries, errorEntry)
		} else if strings.TrimSpace(answer) != "" {
			m.entries = append(m.entries, entry{Role: "assistant", Content: answer, Time: time.Now()})
		}
		m.answerDraft = ""
		m.syncTranscriptViewport(wasAtBottom)
		return m, m.placeTerminalCursor()
	case streamClosedMsg:
		m.stream = nil
		return m, nil
	case answerMsg:
		wasAtBottom := m.transcriptView.AtBottom()
		m.running = false
		m.status = "idle"
		m.resetThinkingBuffer()
		for _, ev := range msg.Events {
			if strings.TrimSpace(ev.Content) == "" {
				continue
			}
			eventEntry := entry{
				Role:    string(ev.Type),
				Content: formatEventContent(ev),
				Time:    time.Now(),
			}
			m.entries = append(m.entries, eventEntry)
		}
		if msg.Err != nil {
			errorEntry := entry{Role: "error", Content: msg.Err.Error(), Time: time.Now()}
			m.entries = append(m.entries, errorEntry)
		} else if strings.TrimSpace(msg.Answer) != "" {
			answerEntry := entry{Role: "assistant", Content: msg.Answer, Time: time.Now()}
			m.entries = append(m.entries, answerEntry)
		}
		m.syncTranscriptViewport(wasAtBottom)
	}
	m.input, cmd = m.input.Update(msg)
	m.sanitizeInput()
	m.updateInputLayout()
	m.syncTranscriptViewport(m.transcriptView.AtBottom())
	return m, tea.Batch(cmd, m.placeTerminalCursor())
}

func (m model) runPrompt(text string, stream chan<- tea.Msg) tea.Cmd {
	return func() tea.Msg {
		var (
			answer string
			err    error
		)
		defer close(stream)
		send := func(msg tea.Msg) {
			select {
			case stream <- msg:
			case <-m.ctx.Done():
			}
		}
		observe := func(event agent.Event) {
			send(streamEventMsg{Event: event})
		}
		switch {
		case strings.HasPrefix(text, "/plan "), strings.EqualFold(text, "/plan"),
			strings.HasPrefix(text, "/team "), strings.EqualFold(text, "/team"):
			answer, err = m.agent.RunCommandWithObserver(m.ctx, text, observe)
		case text == "/help" || text == "/":
			answer = slashHelp()
		default:
			answer, err = m.agent.RunWithObserver(m.ctx, text, observe)
		}
		send(streamDoneMsg{Answer: answer, Err: err})
		return nil
	}
}

func waitForStream(stream <-chan tea.Msg) tea.Cmd {
	return func() tea.Msg {
		if stream == nil {
			return streamClosedMsg{}
		}
		msg, ok := <-stream
		if !ok {
			return streamClosedMsg{}
		}
		return msg
	}
}

func (m model) View() string {
	input := m.inputBox()
	gap := m.inputStatusGap()
	status := m.statusBar()
	banner := m.banner()
	height := max(8, m.height)
	transcriptHeight := m.resolvedTranscriptHeight(banner, input, gap, status, height)
	transcript := m.renderTranscriptViewport(transcriptHeight)
	view := renderFrame(banner, transcript, input, gap, status)
	return view
}

func renderFrame(banner, transcript, input, gap, status string) string {
	return strings.Join([]string{banner, transcript, input, gap, status}, "\n")
}

func (m model) transcriptHeight(banner, input, gap, status string, terminalHeight int) int {
	fixedHeight := lipgloss.Height(banner) + lipgloss.Height(input) + lipgloss.Height(gap) + lipgloss.Height(status) + 4
	return max(1, terminalHeight-fixedHeight)
}

func (m model) resolvedTranscriptHeight(banner, input, gap, status string, terminalHeight int) int {
	height := m.transcriptHeight(banner, input, gap, status, terminalHeight)
	transcript := m.renderTranscriptViewport(height)
	view := renderFrame(banner, transcript, input, gap, status)
	if delta := terminalHeight - lipgloss.Height(view); delta != 0 {
		height = max(1, height+delta)
	}
	return height
}

func (m *model) syncTranscriptViewport(stickToBottom bool) {
	banner := m.banner()
	input := m.inputBox()
	gap := m.inputStatusGap()
	status := m.statusBar()
	height := m.resolvedTranscriptHeight(banner, input, gap, status, max(8, m.height))
	width := max(1, max(40, m.width)-scrollbarWidth)
	m.transcriptView.Width = width
	m.transcriptView.Height = height
	m.transcriptView.SetContent(m.transcript())
	if stickToBottom {
		m.transcriptView.GotoBottom()
	} else if m.transcriptView.PastBottom() {
		m.transcriptView.GotoBottom()
	}
}

func (m model) renderTranscriptViewport(height int) string {
	view := m.transcriptView
	view.Width = max(1, max(40, m.width)-scrollbarWidth)
	view.Height = max(1, height)
	return appendScrollbar(view.View(), m.scrollbar(view))
}

func (m model) scrollbar(view viewport.Model) []string {
	height := max(1, view.Height)
	total := view.TotalLineCount()
	if total <= height {
		return blankScrollbar(height)
	}
	thumbHeight := max(1, height*height/total)
	if thumbHeight > height {
		thumbHeight = height
	}
	maxTop := max(0, height-thumbHeight)
	thumbTop := int(view.ScrollPercent()*float64(maxTop) + 0.5)
	out := make([]string, height)
	for i := range out {
		if i >= thumbTop && i < thumbTop+thumbHeight {
			out[i] = scrollbarThumbStyle.Render("█")
		} else {
			out[i] = scrollbarTrackStyle.Render("│")
		}
	}
	return out
}

func blankScrollbar(height int) []string {
	out := make([]string, height)
	for i := range out {
		out[i] = " "
	}
	return out
}

func appendScrollbar(view string, bar []string) string {
	lines := strings.Split(view, "\n")
	if strings.HasSuffix(view, "\n") && len(lines) > 0 {
		lines = lines[:len(lines)-1]
	}
	height := len(bar)
	if height == 0 {
		height = len(lines)
	}
	for len(lines) < height {
		lines = append(lines, "")
	}
	if len(lines) > height {
		lines = lines[:height]
	}
	for len(bar) < height {
		bar = append(bar, " ")
	}
	for i := range lines {
		lines[i] += bar[i]
	}
	return strings.Join(lines, "\n")
}

func (m *model) handleTranscriptScrollKey(msg tea.KeyMsg) bool {
	switch msg.String() {
	case "pgup":
		m.transcriptView.PageUp()
	case "pgdown":
		m.transcriptView.PageDown()
	default:
		return false
	}
	return true
}

func (m model) banner() string {
	left := logoStyle.Render(strings.Join([]string{
		"██████████",
		"  ██  ██  ",
		"  ██  ██  ",
		"  ██  ██  ",
		"  ██  ██  ",
	}, "\n"))
	info := lipgloss.JoinVertical(lipgloss.Left,
		titleStyle.Render("Zcoder π")+" "+mutedStyle.Render(m.startup.Version),
		mutedStyle.Render(displayModelName(m.startup.Model)),
		boldStyle.Render("MCP")+" "+mutedStyle.Render(fmt.Sprintf("%d ready · %d tools", m.startup.MCPReady, m.startup.MCPTools)),
		boldStyle.Render("Skill")+" "+mutedStyle.Render(fmt.Sprintf("%d/%d enabled", m.startup.SkillsEnabled, m.startup.SkillsTotal)),
	)
	panelWidth := max(40, m.width-lipgloss.Width(left)-lipgloss.Width(info)-8)
	panel := panelStyle.Width(panelWidth).Render(
		sectionStyle.Render("What's ready") + "\n" +
			"- ReAct · Plan · Multi-Agent\n" +
			"- grep · RAG · Web · MCP · Skill\n" +
			"- Runtime · Snapshot · WeChat")
	return lipgloss.JoinHorizontal(lipgloss.Top, left, "  ", info, "  ", panel)
}

func (m *model) updateInputLayout() {
	width := max(40, m.width)
	contentWidth := max(1, width-lipgloss.Width(inputPrompt))
	m.input.SetWidth(width)
	m.input.SetHeight(inputRows(m.input.Value(), contentWidth))
}

func (m model) inputBox() string {
	width := max(40, m.width)
	lines := make([]string, 0, inputPaddingTop+maxInputRows+inputPaddingBottom)
	for range inputPaddingTop {
		lines = append(lines, inputPaddingLine(width))
	}
	if m.input.Value() == "" {
		lines = append(lines, m.emptyInputLine(width))
	} else {
		contentLines := strings.Split(strings.TrimRight(m.input.View(), "\n"), "\n")
		if len(contentLines) == 0 {
			contentLines = []string{""}
		}
		lines = append(lines, contentLines...)
	}
	for range inputPaddingBottom {
		lines = append(lines, inputPaddingLine(width))
	}
	return strings.Join(lines, "\n")
}

func inputPaddingLine(width int) string {
	return inputFillStyle.Render(strings.Repeat(" ", width))
}

func (m model) inputStatusGap() string {
	return strings.Repeat(" ", max(40, m.width))
}

func (m model) placeTerminalCursor() tea.Cmd {
	row, col := m.inputCursorPosition()
	stop := m.cursorStop
	return func() tea.Msg {
		if stop != nil {
			select {
			case <-time.After(terminalCursorPlacementDelay):
			case <-stop:
				return nil
			}
		} else {
			time.Sleep(terminalCursorPlacementDelay)
		}
		_, _ = fmt.Fprintf(os.Stdout, "\x1b[%d;%dH", row, col)
		return nil
	}
}

func (m model) inputCursorPosition() (int, int) {
	row := m.renderedInputContentRow()
	col := lipgloss.Width(inputPrompt) + 1
	if m.input.Value() != "" {
		info := m.input.LineInfo()
		col += info.CharOffset
	}

	return clamp(row, 1, max(1, m.height)), clamp(col, 1, max(1, m.width))
}

func (m model) renderedInputContentRow() int {
	lines := strings.Split(xansi.Strip(m.View()), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.HasPrefix(lines[i], inputPrompt) {
			row := i + 1
			if m.input.Value() != "" {
				row += m.input.LineInfo().RowOffset
			}
			return clamp(row, 1, max(1, len(lines)))
		}
	}
	return max(1, m.height)
}

func (m model) emptyInputLine(width int) string {
	prompt := inputPromptStyle.Render(inputPrompt)
	cursor, rest := splitFirstRune(inputPlaceholder)
	if cursor == "" {
		cursor = " "
	}
	content := prompt + inputCursorStyle.Render(cursor) + inputPlaceholderStyle.Render(rest)
	pad := max(0, width-lipgloss.Width(content))
	return content + inputFillStyle.Render(strings.Repeat(" ", pad))
}

func splitAtVisualColumn(s string, col int) (string, string, string) {
	if col <= 0 {
		under, after := splitFirstRune(s)
		return "", under, after
	}
	var before strings.Builder
	used := 0
	for i, r := range s {
		w := lipgloss.Width(string(r))
		if used >= col {
			under, after := splitFirstRune(s[i:])
			return before.String(), under, after
		}
		if used+w > col {
			return before.String(), "", s[i:]
		}
		before.WriteRune(r)
		used += w
	}
	return before.String(), "", ""
}

func splitFirstRune(s string) (string, string) {
	if s == "" {
		return "", ""
	}
	for i, r := range s {
		if i == 0 {
			size := len(string(r))
			return string(r), s[size:]
		}
	}
	return "", ""
}

func inputRows(value string, width int) int {
	if width <= 0 {
		return 1
	}
	rows := 1
	for _, line := range strings.Split(value, "\n") {
		lineWidth := lipgloss.Width(line)
		if lineWidth > 0 {
			rows += lineWidth / width
			if lineWidth%width == 0 {
				rows--
			}
		}
	}
	if rows < 1 {
		return 1
	}
	if rows > maxInputRows {
		return maxInputRows
	}
	return rows
}

func newMarkdownRenderer(width int) (*glamour.TermRenderer, error) {
	return glamour.NewTermRenderer(glamour.WithStandardStyle("dark"), glamour.WithWordWrap(width))
}

func isTerminalControlResponse(msg tea.KeyMsg) bool {
	s := msg.String()
	if len(msg.Runes) > 0 {
		s += string(msg.Runes)
	}
	return terminalMouseResponseRE.MatchString(s) ||
		strings.Contains(s, "]11;") ||
		strings.Contains(s, "]10;") ||
		strings.Contains(s, "]12;") ||
		strings.Contains(s, "rgb:") ||
		strings.Contains(s, "\x1b]") ||
		strings.Contains(s, "\x9d")
}

func (m model) isRecentMouseControlFragment(msg tea.KeyMsg) bool {
	if m.lastMouseEvent.IsZero() || time.Since(m.lastMouseEvent) > terminalControlFragmentWindow {
		return false
	}
	s := msg.String()
	if len(msg.Runes) > 0 {
		s = string(msg.Runes)
	}
	return s != "" && strings.Trim(s, "[") == ""
}

func (m *model) sanitizeInput() {
	value := m.input.Value()
	clean := stripTerminalControlResponses(value)
	if clean != value {
		m.input.SetValue(clean)
	}
}

var (
	terminalControlResponseRE = regexp.MustCompile(`(?s)(?:\x1b\]|\])?(?:10|11|12);rgb:[0-9a-fA-F]{1,4}/[0-9a-fA-F]{1,4}/[0-9a-fA-F]{1,4}(?:\x1b\\|\\)?`)
	terminalMouseResponseRE   = regexp.MustCompile(`(?:\x1b\[|\x9b|\[)?<\d{1,3};\d{1,4};\d{1,4}[mM]`)
)

const terminalControlFragmentWindow = 300 * time.Millisecond
const terminalCursorPlacementDelay = 20 * time.Millisecond

func stripTerminalControlResponses(s string) string {
	s = terminalControlResponseRE.ReplaceAllString(s, "")
	s = terminalMouseResponseRE.ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, "]11;", "")
	s = strings.ReplaceAll(s, "]10;", "")
	s = strings.ReplaceAll(s, "]12;", "")
	return strings.TrimLeft(s, "\x1b\\] ")
}

func (m model) transcript() string {
	var b strings.Builder
	for i, e := range m.entries {
		if i > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(m.renderEntry(e))
	}
	return b.String()
}

func (m model) renderEntry(e entry) string {
	switch e.Role {
	case "user":
		return userStyle.Width(max(40, m.width-2)).Render("> " + e.Content)
	case "error":
		return errorStyle.Render("Error: " + e.Content)
	case string(agent.EventThinking):
		return thinkingEntryStyle.Width(max(40, m.width-2)).Render(e.Content)
	case string(agent.EventToolCall):
		return toolCallEntryStyle.Width(max(40, m.width-2)).Render(e.Content)
	case string(agent.EventToolResult):
		return toolResultEntryStyle.Width(max(40, m.width-2)).Render(e.Content)
	default:
		content := e.Content
		if m.renderer != nil {
			if rendered, err := m.renderer.Render(content); err == nil {
				content = strings.TrimRight(rendered, "\n")
			}
		}
		return assistantStyle.Render(content)
	}
}

func (m *model) applyStreamEvent(ev agent.Event) string {
	switch ev.Type {
	case agent.EventThinkingDelta:
		title := strings.TrimSpace(ev.Title)
		if title == "" {
			title = "Thinking"
		}
		m.appendStreamDelta(string(agent.EventThinking), sectionStyle.Render(title)+"\n", ev.Content)
		return m.appendThinkingLine(title, ev.Content)
	case agent.EventAnswerDelta:
		if strings.TrimSpace(ev.Content) == "" {
			return ""
		}
		m.answerDraft += ev.Content
		return ""
	case agent.EventThinking, agent.EventToolCall, agent.EventToolResult:
		if strings.TrimSpace(ev.Content) == "" {
			return ""
		}
		outputs := compactOutputs(m.flushThinkingLine())
		eventEntry := entry{
			Role:    string(ev.Type),
			Content: formatEventContent(ev),
			Time:    time.Now(),
		}
		m.entries = append(m.entries, eventEntry)
		outputs = append(outputs, m.renderEntry(eventEntry))
		return strings.Join(outputs, "\n\n")
	}
	return ""
}

func (m *model) appendThinkingLine(title, delta string) string {
	if strings.TrimSpace(delta) == "" {
		return ""
	}
	var outputs []string
	if m.thinkingTitle != "" && m.thinkingTitle != title {
		outputs = append(outputs, m.flushThinkingLine())
		m.thinkingLine = ""
		m.thinkingTitle = title
		m.thinkingHeader = false
	}
	if m.thinkingTitle == "" {
		m.thinkingTitle = title
	}
	m.thinkingLine += strings.ReplaceAll(delta, "\r", "")
	for {
		idx := strings.Index(m.thinkingLine, "\n")
		if idx < 0 {
			break
		}
		line := strings.TrimSpace(m.thinkingLine[:idx])
		m.thinkingLine = m.thinkingLine[idx+1:]
		if line != "" {
			outputs = append(outputs, m.renderThinkingLine(line))
		}
	}
	for lipgloss.Width(strings.TrimSpace(m.thinkingLine)) >= m.thinkingFlushWidth() {
		line, rest := splitThinkingLine(m.thinkingLine, m.thinkingFlushWidth())
		if strings.TrimSpace(line) == "" {
			break
		}
		outputs = append(outputs, m.renderThinkingLine(strings.TrimSpace(line)))
		m.thinkingLine = rest
	}
	if endsThinkingSentence(m.thinkingLine) {
		outputs = append(outputs, m.flushThinkingLine())
	}
	return strings.Join(compactOutputs(outputs...), "\n")
}

func (m *model) flushThinkingLine() string {
	line := strings.TrimSpace(m.thinkingLine)
	m.thinkingLine = ""
	if line == "" {
		return ""
	}
	return m.renderThinkingLine(line)
}

func (m *model) renderThinkingLine(line string) string {
	content := line
	if !m.thinkingHeader {
		title := m.thinkingTitle
		if title == "" {
			title = "Thinking"
		}
		content = sectionStyle.Render(title) + "\n" + line
		m.thinkingHeader = true
	}
	return thinkingEntryStyle.Render(content)
}

func (m *model) resetThinkingBuffer() {
	m.thinkingTitle = ""
	m.thinkingLine = ""
	m.thinkingHeader = false
}

func (m model) thinkingFlushWidth() int {
	width := m.width - 10
	if width < 40 {
		return 40
	}
	if width > 96 {
		return 96
	}
	return width
}

func splitThinkingLine(s string, width int) (string, string) {
	before, under, after := splitAtVisualColumn(s, width)
	if strings.TrimSpace(before) == "" {
		return s, ""
	}
	return before, under + after
}

func endsThinkingSentence(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	runes := []rune(s)
	last := runes[len(runes)-1]
	return strings.ContainsRune("，,；;。.!！？?", last)
}

func compactOutputs(outputs ...string) []string {
	compacted := make([]string, 0, len(outputs))
	for _, output := range outputs {
		if strings.TrimSpace(output) != "" {
			compacted = append(compacted, output)
		}
	}
	return compacted
}

func (m *model) appendStreamDelta(role, prefix, delta string) {
	if delta == "" {
		return
	}
	if len(m.entries) > 0 {
		last := &m.entries[len(m.entries)-1]
		if last.Role == role && strings.HasPrefix(last.Content, prefix) {
			last.Content += delta
			return
		}
	}
	m.entries = append(m.entries, entry{Role: role, Content: prefix + delta, Time: time.Now()})
}

func formatEventContent(ev agent.Event) string {
	title := strings.TrimSpace(ev.Title)
	content := strings.TrimSpace(ev.Content)
	switch ev.Type {
	case agent.EventThinking:
		if title == "" {
			title = "Thinking"
		}
		return sectionStyle.Render(title) + "\n" + content
	case agent.EventToolCall:
		if title == "" {
			title = "tool"
		}
		return toolCallHeaderStyle.Render("Tool use: "+title) + "\n" + content
	case agent.EventToolResult:
		if title == "" {
			title = "tool"
		}
		return toolResultHeaderStyle.Render("Tool result: "+title) + "\n" + content
	default:
		if title == "" {
			return content
		}
		return title + "\n" + content
	}
}

func (m model) statusBar() string {
	width := max(40, m.width)
	ctxUsed := m.estimatedContextTokens()
	ctxMax := m.startup.MaxContext
	if ctxMax <= 0 {
		ctxMax = 128000
	}
	modelName := displayModelName(m.startup.Model)
	left := modeStyle.Render(m.mode)
	if modelName != "" {
		left += " " + modelStyle.Render(modelName)
	}
	rightMax := max(0, width-lipgloss.Width(left)-1)
	right := m.statusRight(ctxUsed, ctxMax, rightMax)
	gap := width - lipgloss.Width(left) - lipgloss.Width(right) - 1
	if gap < 1 {
		gap = 1
	}
	line := left + strings.Repeat(" ", gap) + right
	if lipgloss.Width(line) > width {
		line = xansi.Truncate(line, width, "")
	}
	return statusStyle.Render(line)
}

func (m model) statusRight(ctxUsed, ctxMax, width int) string {
	if width <= 0 {
		return ""
	}
	right := m.contextStatus(ctxUsed, ctxMax)
	pathWidth := width - lipgloss.Width(right) - 1
	if pathWidth >= 8 {
		right += " " + mutedStyle.Render(truncateMiddle(m.startup.CWD, pathWidth))
	}
	if lipgloss.Width(right) > width {
		return xansi.Truncate(right, width, "")
	}
	return right
}

func (m model) estimatedContextTokens() int {
	chars := 0
	for _, e := range m.entries {
		chars += len([]rune(e.Role)) + len([]rune(e.Content)) + 8
	}
	chars += len([]rune(m.answerDraft))
	chars += len([]rune(m.input.Value()))
	// Add a conservative fixed overhead for system prompt, tool definitions,
	// skill index and runtime context. The exact provider-side count is only
	// known after a model call, but this keeps the bar directionally useful.
	return 1200 + chars/3
}

func (m model) contextStatus(used, window int) string {
	if used < 0 {
		used = 0
	}
	if used > window {
		used = window
	}
	percent := 0
	if window > 0 {
		percent = int(float64(used) / float64(window) * 100)
	}
	if percent == 0 && used > 0 {
		percent = 1
	}
	return okStyle.Render("ctx") + " " +
		progressBar(used, window, 12) + " " +
		mutedStyle.Render(fmt.Sprintf("%d%% %s/%s", percent, compactTokenCount(used), compactTokenCount(window)))
}

func displayModelName(model string) string {
	model = strings.TrimSpace(model)
	if model == "" {
		return ""
	}
	parts := strings.FieldsFunc(model, func(r rune) bool {
		return r == '-' || r == '_' || r == '/' || r == ' '
	})
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if part == "" {
			continue
		}
		lower := strings.ToLower(part)
		switch lower {
		case "deepseek":
			out = append(out, "DeepSeek")
		case "glm":
			out = append(out, "GLM")
		case "gpt":
			out = append(out, "GPT")
		case "v1", "v2", "v3", "v4", "v5":
			out = append(out, strings.ToUpper(lower))
		default:
			out = append(out, titleToken(lower))
		}
	}
	return strings.Join(out, " ")
}

func titleToken(s string) string {
	if s == "" {
		return ""
	}
	r := []rune(s)
	return strings.ToUpper(string(r[:1])) + string(r[1:])
}

func progressBar(used, window, width int) string {
	if width <= 0 {
		return ""
	}
	filled := 0
	if window > 0 {
		filled = int(float64(used) / float64(window) * float64(width))
	}
	if used > 0 && filled == 0 {
		filled = 1
	}
	if filled > width {
		filled = width
	}
	return progressFillStyle.Render(strings.Repeat("█", filled)) +
		progressEmptyStyle.Render(strings.Repeat(" ", width-filled))
}

func compactTokenCount(n int) string {
	if n >= 1000000 {
		return fmt.Sprintf("%.1fM", float64(n)/1000000)
	}
	if n >= 1000 {
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	}
	return fmt.Sprintf("%d", n)
}

func truncateMiddle(s string, width int) string {
	if width <= 0 || lipgloss.Width(s) <= width {
		return s
	}
	if width <= 3 {
		return strings.Repeat(".", width)
	}
	r := []rune(s)
	keep := width - 3
	left := keep / 2
	right := keep - left
	if left+right >= len(r) {
		return s
	}
	return string(r[:left]) + "..." + string(r[len(r)-right:])
}

func slashHelp() string {
	return strings.TrimSpace(`
Zcoder commands:

- /plan <task>  Run Plan-and-Execute
- /team <task>  Run Multi-Agent workflow
- /help         Show this help
- /exit         Quit

CLI commands outside the TUI:

- zcoder doctor
- zcoder index
- zcoder search <query>
- zcoder serve --port 8080
- zcoder wechat status
`)
}

const (
	inputPrompt        = "* "
	inputPlaceholder   = "Type your message or @path/to/file"
	inputPaddingTop    = 1
	inputPaddingBottom = 1
	maxInputRows       = 4
	scrollbarWidth     = 1
)

var (
	logoStyle             = lipgloss.NewStyle().Foreground(lipgloss.Color("42")).Bold(true)
	titleStyle            = lipgloss.NewStyle().Foreground(lipgloss.Color("15")).Bold(true)
	sectionStyle          = lipgloss.NewStyle().Foreground(lipgloss.Color("42")).Bold(true)
	boldStyle             = lipgloss.NewStyle().Bold(true)
	mutedStyle            = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	panelStyle            = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("238")).Padding(0, 1)
	userStyle             = lipgloss.NewStyle().Background(lipgloss.Color("236")).Foreground(lipgloss.Color("252")).Padding(0, 1)
	assistantStyle        = lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	errorStyle            = lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Bold(true)
	inputFillStyle        = lipgloss.NewStyle().Background(lipgloss.Color("236"))
	inputCursorStyle      = lipgloss.NewStyle().Background(lipgloss.Color("15")).Foreground(lipgloss.Color("236"))
	inputPromptStyle      = lipgloss.NewStyle().Background(lipgloss.Color("236")).Foreground(lipgloss.Color("252"))
	inputTextStyle        = lipgloss.NewStyle().Background(lipgloss.Color("236")).Foreground(lipgloss.Color("252"))
	inputPlaceholderStyle = lipgloss.NewStyle().
				Background(lipgloss.Color("236")).
				Foreground(lipgloss.Color("244"))
	statusStyle         = lipgloss.NewStyle()
	modeStyle           = lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Bold(true)
	modelStyle          = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	okStyle             = lipgloss.NewStyle().Foreground(lipgloss.Color("42")).Bold(true)
	scrollbarTrackStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("238"))
	scrollbarThumbStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("39"))
	thinkingStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("220")).Bold(true)
	thinkingEntryStyle = lipgloss.NewStyle().
				Border(lipgloss.NormalBorder(), false, false, false, true).
				BorderForeground(lipgloss.Color("220")).
				Foreground(lipgloss.Color("252")).
				PaddingLeft(1)
	toolCallEntryStyle = lipgloss.NewStyle().
				Border(lipgloss.NormalBorder(), false, false, false, true).
				BorderForeground(lipgloss.Color("39")).
				Foreground(lipgloss.Color("252")).
				PaddingLeft(1)
	toolResultEntryStyle = lipgloss.NewStyle().
				Border(lipgloss.NormalBorder(), false, false, false, true).
				BorderForeground(lipgloss.Color("244")).
				Foreground(lipgloss.Color("245")).
				PaddingLeft(1)
	toolCallHeaderStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("39")).Bold(true)
	toolResultHeaderStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Bold(true)
	progressFillStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	progressEmptyStyle    = lipgloss.NewStyle().Background(lipgloss.Color("236"))
)

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
