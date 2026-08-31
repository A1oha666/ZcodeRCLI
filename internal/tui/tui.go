package tui

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
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
	clusterRun     ClusterRunFunc
	cluster        *clusterPanel
	startup        Startup
	input          textarea.Model
	spinner        spinner.Model
	runStarted     time.Time
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

func Run(ctx context.Context, ag *agent.Agent, startup Startup, clusterRun ClusterRunFunc) error {
	cursorStop := make(chan struct{})
	m := newModel(ctx, ag, startup, clusterRun)
	m.cursorStop = cursorStop
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion(), tea.WithOutput(os.Stdout))
	_, err := p.Run()
	close(cursorStop)
	return err
}

func newModel(ctx context.Context, ag *agent.Agent, startup Startup, clusterRun ClusterRunFunc) model {
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
	sp := spinner.New(spinner.WithSpinner(spinner.Points), spinner.WithStyle(lipgloss.NewStyle().Foreground(colorWarn)))
	transcriptView := viewport.New(79, 20)
	transcriptView.MouseWheelEnabled = true
	transcriptView.MouseWheelDelta = 3
	renderer, _ := newMarkdownRenderer(100)
	m := model{
		ctx:            ctx,
		agent:          ag,
		clusterRun:     clusterRun,
		startup:        startup,
		input:          input,
		spinner:        sp,
		transcriptView: transcriptView,
		width:          100,
		height:         30,
		mode:           "YOLO",
		status:         "idle",
		renderer:       renderer,
		entries: []entry{{
			Role:    "assistant",
			Content: welcomeMessage(),
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
			m.runStarted = time.Now()
			if isClusterCommand(text) {
				stream := make(chan tea.Msg, 256)
				m.stream = stream
				return m.startCluster(text, stream)
			}
			m.running = true
			m.status = "running"
			m.answerDraft = ""
			m.resetThinkingBuffer()
			m.syncTranscriptViewport(true)
			stream := make(chan tea.Msg, 256)
			m.stream = stream
			return m, tea.Batch(m.runPrompt(text, stream), waitForStream(stream), m.spinner.Tick, m.placeTerminalCursor())
		}
	case tea.MouseMsg:
		m.lastMouseEvent = time.Now()
		m.transcriptView, cmd = m.transcriptView.Update(msg)
		return m, tea.Batch(cmd, m.placeTerminalCursor())
	case spinner.TickMsg:
		if m.running {
			m.spinner, cmd = m.spinner.Update(msg)
			return m, cmd
		}
		return m, nil
	case clusterEventMsg:
		wasAtBottom := m.transcriptView.AtBottom()
		m.applyClusterEvent(msg.Event)
		m.syncTranscriptViewport(wasAtBottom)
		return m, waitForStream(m.stream)
	case streamEventMsg:
		wasAtBottom := m.transcriptView.AtBottom()
		m.applyStreamEvent(msg.Event)
		m.syncTranscriptViewport(wasAtBottom)
		return m, waitForStream(m.stream)
	case streamDoneMsg:
		wasAtBottom := m.transcriptView.AtBottom()
		m.finishCluster()
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
	status := m.statusBar()
	banner := m.banner()
	height := max(8, m.height)
	transcriptHeight := m.resolvedTranscriptHeight(banner, input, status, height)
	transcript := m.renderTranscriptViewport(transcriptHeight)
	view := renderFrame(banner, transcript, input, status)
	return view
}

func renderFrame(banner, transcript, input, status string) string {
	return strings.Join([]string{banner, transcript, input, status}, "\n")
}

func (m model) transcriptHeight(banner, input, status string, terminalHeight int) int {
	fixedHeight := lipgloss.Height(banner) + lipgloss.Height(input) + lipgloss.Height(status) + 4
	return max(1, terminalHeight-fixedHeight)
}

func (m model) resolvedTranscriptHeight(banner, input, status string, terminalHeight int) int {
	height := m.transcriptHeight(banner, input, status, terminalHeight)
	transcript := m.renderTranscriptViewport(height)
	view := renderFrame(banner, transcript, input, status)
	if delta := terminalHeight - lipgloss.Height(view); delta != 0 {
		height = max(1, height+delta)
	}
	return height
}

func (m *model) syncTranscriptViewport(stickToBottom bool) {
	banner := m.banner()
	input := m.inputBox()
	status := m.statusBar()
	height := m.resolvedTranscriptHeight(banner, input, status, max(8, m.height))
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

// banner renders the ZCODER block-letter logo (Z in brand indigo, CODER in
// cyan) with the version right-aligned on its last row, plus the model /
// MCP / skill status chips underneath. Narrow terminals fall back to a
// one-line wordmark.
func (m model) banner() string {
	meta := make([]string, 0, 4)
	if name := displayModelName(m.startup.Model); name != "" {
		meta = append(meta, modelStyle.Render(name))
	}
	meta = append(meta, mutedStyle.Render(fmt.Sprintf("MCP %d/%d", m.startup.MCPReady, m.startup.MCPTools)))
	meta = append(meta, mutedStyle.Render(fmt.Sprintf("Skills %d/%d", m.startup.SkillsEnabled, m.startup.SkillsTotal)))
	metaLine := "  " + strings.Join(meta, faintStyle.Render("  ·  "))

	width := max(40, m.width)
	if width < asciiLogoMinWidth {
		brand := brandStyle.Render("Z") + toolCallHeaderStyle.Render("CODER")
		if v := strings.TrimSpace(m.startup.Version); v != "" {
			brand += " " + faintStyle.Render(v)
		}
		return brand + "\n" + metaLine
	}

	rows := asciiLogoRows()
	if v := strings.TrimSpace(m.startup.Version); v != "" {
		version := faintStyle.Render(v)
		last := len(rows) - 1
		if gap := width - asciiLogoWidth - lipgloss.Width(version) - 2; gap > 0 {
			rows[last] += strings.Repeat(" ", gap) + version
		}
	}
	return strings.Join(rows, "\n") + "\n" + metaLine
}

const asciiLogoMinWidth = 58

const asciiLogoWidth = 52

// asciiLogoRows renders the ZCODER wordmark: Z in brand indigo, CODER in
// cyan.
func asciiLogoRows() []string {
	z := []string{
		"███████╗",
		"╚═══██╔╝",
		"  ███╔╝ ",
		" ██╔╝   ",
		"███████╗",
		"╚══════╝",
	}
	rest := []string{
		" █████╗ █████╗ █████╗ ███████╗██████╗",
		"██╔═══╝██╔══██╗██╔══██╗██╔═══╝██╔══██╗",
		"██║    ██║  ██║██║  ██║█████╗ ██████╔╝",
		"██║    ██║  ██║██║  ██║██╔══╝ ██╔══██╗",
		"╚█████╗╚█████╔╝█████╔╝ ███████╗██║  ██║",
		" ╚════╝ ╚════╝ ╚════╝  ╚══════╝╚═╝  ╚═╝",
	}
	rows := make([]string, len(z))
	for i := range z {
		rows[i] = brandStyle.Render(z[i]) + toolCallHeaderStyle.Render(rest[i])
	}
	return rows
}

func welcomeMessage() string {
	return "你好！我是 **ZcodeR**，你的终端 AI 编程助手。\n\n" +
		"我可以读写代码、执行命令、搜索代码库、联网检索，也支持多 Agent 协作。\n\n" +
		"常用命令：\n\n" +
		"- `/plan` — 先规划后执行复杂任务\n" +
		"- `/team` — 多 Agent 协作\n" +
		"- `/cluster` — 并发 Agent 集群（实验性）\n" +
		"- `/help` — 查看全部命令"
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

// newMarkdownRenderer is kept for tests that exercise renderer lifecycle.
// Assistant answers are rendered by renderAssistantMarkdown.
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
	panel := m.renderClusterPanel()
	if panel != "" {
		b.WriteString("\n\n" + panel)
	}
	if len(m.entries) == 0 && panel == "" {
		b.WriteString(faintStyle.Render("暂无对话记录，输入消息开始对话，/help 查看命令。"))
	}
	return b.String()
}

func (m model) renderEntry(e entry) string {
	width := max(40, m.width-scrollbarWidth-2)
	switch e.Role {
	case "user":
		stamp := timeStyle.Render(e.Time.Format("15:04"))
		return userStyle.Width(width).Render("❯ "+e.Content) + " " + stamp
	case "error":
		return errorStyle.Render("✗ " + e.Content)
	case string(agent.EventThinking):
		return thinkingEntryStyle.Width(width).Render(e.Content)
	case string(agent.EventToolCall):
		return toolCallEntryStyle.Width(width).Render(e.Content)
	case string(agent.EventToolResult):
		return toolResultEntryStyle.Width(width).Render(e.Content)
	default:
		content := e.Content
		if m.renderer != nil {
			content = renderAssistantMarkdown(content, max(40, m.width-scrollbarWidth-4))
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
		m.appendStreamDelta(string(agent.EventThinking), thinkingHeaderStyle.Render(title)+"\n", ev.Content)
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
		content = thinkingHeaderStyle.Render(title) + "\n" + line
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
		return thinkingHeaderStyle.Render(title) + "\n" + content
	case agent.EventToolCall:
		if title == "" {
			title = "tool"
		}
		return toolCallHeaderStyle.Render("$ "+title) + "\n" + content
	case agent.EventToolResult:
		if title == "" {
			title = "tool"
		}
		return toolResultHeaderStyle.Render("← "+title) + "\n" + content
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
	left := " " + modeStyle.Render(m.mode)
	if modelName != "" {
		left += " " + modelStyle.Render(modelName)
	}
	if m.running {
		elapsed := ""
		if !m.runStarted.IsZero() {
			elapsed = " · " + time.Since(m.runStarted).Round(time.Second).String()
		}
		hint := ""
		if m.status == "cancel requested" {
			hint = " · 正在取消"
		}
		left += "  " + m.spinner.View() + " " + okStyle.Render("思考中") + mutedStyle.Render(elapsed+hint)
	}
	rightMax := max(0, width-lipgloss.Width(left)-1)
	right := m.statusRight(ctxUsed, ctxMax, rightMax)
	gap := width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		gap = 1
	}
	line := left + strings.Repeat(" ", gap) + right
	if lipgloss.Width(line) > width {
		line = xansi.Truncate(line, width, "")
	}
	return faintStyle.Render(line)
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
		progressEmptyStyle.Render(strings.Repeat("░", width-filled))
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
- /cluster [--agents N] [--concurrency N] [--simulate] <task>  Run an agent cluster with live progress
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

// renderAssistantMarkdown renders model output as clean plain text: fenced
// code blocks are kept verbatim and indented, list markers become bullets,
// emphasis markers are stripped, and everything is word-wrapped by display
// width. Glamour was dropped here because its colorless style drops list
// bullets and its default theme paints inline code red (our error color).
func renderAssistantMarkdown(content string, width int) string {
	if width < 20 {
		width = 20
	}
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	var out []string
	inCode := false
	pendingBlank := false
	prevList := false
	flushBlank := func() {
		if pendingBlank && len(out) > 0 {
			out = append(out, "")
		}
		pendingBlank = false
	}
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			inCode = !inCode
			continue
		}
		if inCode {
			flushBlank()
			out = append(out, "  "+line)
			continue
		}
		if trimmed == "" {
			pendingBlank = true
			prevList = false
			continue
		}
		if isHorizontalRule(trimmed) {
			flushBlank()
			prevList = false
			out = append(out, faintStyle.Render(strings.Repeat("─", min(width, 40))))
			continue
		}
		indent, body, ordered, isList := splitListMarker(line)
		if isList && !pendingBlank && !prevList && len(out) > 0 {
			// Visual separator between a paragraph and the list that follows it.
			out = append(out, "")
		}
		flushBlank()
		prevList = isList
		body = stripMarkdownEmphasis(body)
		prefix := indent
		if isList {
			if ordered {
				prefix += body[:strings.IndexByte(body, ' ')] + " "
				body = body[strings.IndexByte(body, ' ')+1:]
			} else {
				prefix += "• "
			}
		}
		out = append(out, wrapMarkdownLine(prefix, body, width)...)
	}
	return strings.TrimRight(strings.Join(out, "\n"), "\n ")
}

func isHorizontalRule(s string) bool {
	if len(s) < 3 {
		return false
	}
	for _, r := range s {
		if r != '-' && r != '*' && r != '_' && r != ' ' {
			return false
		}
	}
	return strings.ContainsAny(s, "- *_")
}

// splitListMarker detects markdown list items and returns the leading indent,
// the item body, whether it is ordered, and whether it is a list item at all.
func splitListMarker(line string) (indent, body string, ordered, ok bool) {
	i := 0
	for i < len(line) && (line[i] == ' ' || line[i] == '\t') {
		i++
	}
	indent = line[:i]
	rest := line[i:]
	if len(rest) >= 2 && (rest[0] == '-' || rest[0] == '*' || rest[0] == '+') && rest[1] == ' ' {
		return indent, rest[2:], false, true
	}
	j := 0
	for j < len(rest) && rest[j] >= '0' && rest[j] <= '9' {
		j++
	}
	if j > 0 && j+1 < len(rest) && rest[j] == '.' && rest[j+1] == ' ' {
		return indent, rest, true, true
	}
	return "", line, false, false
}

// stripMarkdownEmphasis removes inline formatting markers, keeping the text.
func stripMarkdownEmphasis(s string) string {
	s = inlineCodeRE.ReplaceAllString(s, "$1")
	s = markdownLinkRE.ReplaceAllString(s, "$1")
	for _, marker := range []string{"**", "__", "~~"} {
		s = strings.ReplaceAll(s, marker, "")
	}
	var b strings.Builder
	runes := []rune(s)
	for i, r := range runes {
		if (r == '*' || r == '_') && (i == 0 || runes[i-1] == ' ' || runes[i-1] == '（' || runes[i-1] == '(') {
			continue
		}
		if (r == '*' || r == '_') && i == len(runes)-1 {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// wrapMarkdownLine wraps body by display width; continuation lines align
// under the text start of the first line.
func wrapMarkdownLine(prefix, body string, width int) []string {
	prefixWidth := lipgloss.Width(prefix)
	words := strings.Fields(body)
	if len(words) == 0 {
		return []string{strings.TrimRight(prefix, " ")}
	}
	indent := strings.Repeat(" ", prefixWidth)
	lines := make([]string, 0, 2)
	cur := prefix + words[0]
	curWidth := prefixWidth + lipgloss.Width(words[0])
	for _, w := range words[1:] {
		wWidth := lipgloss.Width(w)
		if curWidth+1+wWidth > width && lipgloss.Width(strings.TrimSpace(cur)) > 0 {
			lines = append(lines, cur)
			cur = indent + w
			curWidth = prefixWidth + wWidth
			continue
		}
		cur += " " + w
		curWidth += 1 + wWidth
	}
	lines = append(lines, cur)
	return lines
}

var (
	inlineCodeRE   = regexp.MustCompile("`([^`]*)`")
	markdownLinkRE = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`)
)

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

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
