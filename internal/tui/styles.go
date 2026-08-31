package tui

import "github.com/charmbracelet/lipgloss"

// Semantic palette. One place to tune the whole interface; component styles
// below only reference these tokens, never raw colors.
var (
	colorBrand     = lipgloss.Color("69")  // indigo — logo, mode, brand accents
	colorUser      = lipgloss.Color("39")  // blue — user messages
	colorThinking  = lipgloss.Color("141") // mauve — reasoning traces
	colorTool      = lipgloss.Color("80")  // cyan — tool calls
	colorToolMuted = lipgloss.Color("66")  // dim cyan — tool results
	colorOK        = lipgloss.Color("78")  // green — success, done
	colorWarn      = lipgloss.Color("221") // yellow — running, in-flight
	colorErr       = lipgloss.Color("203") // red — errors, failed
	colorText      = lipgloss.Color("255") // primary text
	colorMuted     = lipgloss.Color("245") // secondary text
	colorFaint     = lipgloss.Color("239") // borders, scrollbar track
	colorFill      = lipgloss.Color("236") // input background
	colorCursor    = lipgloss.Color("15")  // block cursor
)

const (
	inputPrompt        = "❯ "
	inputPlaceholder   = "给 ZcodeR 发送消息，或输入 / 查看命令"
	inputPaddingTop    = 1
	inputPaddingBottom = 1
	maxInputRows       = 4
	scrollbarWidth     = 1
)

var (
	brandStyle = lipgloss.NewStyle().Foreground(colorBrand).Bold(true)
	mutedStyle = lipgloss.NewStyle().Foreground(colorMuted)
	faintStyle = lipgloss.NewStyle().Foreground(colorFaint)
)

var (
	userStyle      = lipgloss.NewStyle().Foreground(colorUser).Bold(true)
	assistantStyle = lipgloss.NewStyle().Foreground(colorText)
	timeStyle      = lipgloss.NewStyle().Foreground(colorMuted)
	errorStyle     = lipgloss.NewStyle().Foreground(colorErr).Bold(true)
)

var (
	inputFillStyle        = lipgloss.NewStyle().Background(colorFill)
	inputCursorStyle      = lipgloss.NewStyle().Background(colorCursor).Foreground(colorFill)
	inputPromptStyle      = lipgloss.NewStyle().Background(colorFill).Foreground(colorBrand).Bold(true)
	inputTextStyle        = lipgloss.NewStyle().Background(colorFill).Foreground(colorText)
	inputPlaceholderStyle = lipgloss.NewStyle().Background(colorFill).Foreground(colorMuted)
)

var (
	modeStyle           = lipgloss.NewStyle().Foreground(colorBrand).Bold(true)
	modelStyle          = lipgloss.NewStyle().Foreground(colorMuted)
	okStyle             = lipgloss.NewStyle().Foreground(colorOK).Bold(true)
	progressFillStyle   = lipgloss.NewStyle().Foreground(colorOK)
	progressEmptyStyle  = lipgloss.NewStyle().Foreground(colorFaint)
	scrollbarTrackStyle = lipgloss.NewStyle().Foreground(colorFaint)
	scrollbarThumbStyle = lipgloss.NewStyle().Foreground(colorMuted)
)

// Left-accent entry styles: the border color carries the semantics so each
// stream type can be told apart at a glance while scrolling.
var (
	thinkingEntryStyle = lipgloss.NewStyle().
				Border(lipgloss.NormalBorder(), false, false, false, true).
				BorderForeground(colorThinking).
				Foreground(colorMuted).
				PaddingLeft(1)
	toolCallEntryStyle = lipgloss.NewStyle().
				Border(lipgloss.NormalBorder(), false, false, false, true).
				BorderForeground(colorTool).
				Foreground(colorText).
				PaddingLeft(1)
	toolResultEntryStyle = lipgloss.NewStyle().
				Border(lipgloss.NormalBorder(), false, false, false, true).
				BorderForeground(colorToolMuted).
				Foreground(colorMuted).
				PaddingLeft(1)
	thinkingHeaderStyle   = lipgloss.NewStyle().Foreground(colorThinking).Bold(true)
	toolCallHeaderStyle   = lipgloss.NewStyle().Foreground(colorTool).Bold(true)
	toolResultHeaderStyle = lipgloss.NewStyle().Foreground(colorToolMuted).Bold(true)
)

var (
	clusterPanelStyle   = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(colorFaint).Padding(0, 1)
	clusterDoneStyle    = lipgloss.NewStyle().Foreground(colorOK)
	clusterFailedStyle  = lipgloss.NewStyle().Foreground(colorErr)
	clusterRunningStyle = lipgloss.NewStyle().Foreground(colorWarn)
	clusterEmptyStyle   = lipgloss.NewStyle().Foreground(colorFaint)
)
