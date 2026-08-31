package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/itwanger/zcoder-go/internal/cluster"
)

// ClusterRunFunc starts a cluster run; main.go injects it so the TUI does not
// need to know how the LLM client is wired up.
type ClusterRunFunc func(ctx context.Context, opts cluster.CommandOptions, task string, observe cluster.Observer) (cluster.Report, error)

type clusterEventMsg struct {
	Event cluster.Event
}

// clusterPanel is the live progress state for the /cluster slash command.
type clusterPanel struct {
	task    string
	started time.Time
	total   int
	done    int
	failed  int
	running int
	recent  []string
}

const clusterRecentLines = 6

func isClusterCommand(text string) bool {
	return text == "/cluster" || strings.HasPrefix(text, "/cluster ")
}

// startCluster handles the /cluster slash command. It returns the batch of
// commands that drives the cluster run over the same stream channel used by
// normal agent turns.
func (m *model) startCluster(text string, stream chan tea.Msg) (tea.Model, tea.Cmd) {
	opts, task, err := cluster.ParseClusterCommand(text)
	if err != nil {
		m.entries = append(m.entries, entry{Role: "error", Content: err.Error(), Time: time.Now()})
		m.running = false
		m.syncTranscriptViewport(true)
		return m, m.placeTerminalCursor()
	}
	if m.clusterRun == nil {
		m.entries = append(m.entries, entry{Role: "error", Content: "cluster runner is not available in this session", Time: time.Now()})
		m.running = false
		m.syncTranscriptViewport(true)
		return m, m.placeTerminalCursor()
	}
	m.cluster = &clusterPanel{
		task:    task,
		started: time.Now(),
		total:   opts.Agents,
	}
	m.running = true
	m.status = "running"
	m.resetThinkingBuffer()
	m.syncTranscriptViewport(true)
	return m, tea.Batch(m.runCluster(opts, task, stream), waitForStream(stream), m.spinner.Tick, m.placeTerminalCursor())
}

func (m model) runCluster(opts cluster.CommandOptions, task string, stream chan<- tea.Msg) tea.Cmd {
	return func() tea.Msg {
		defer close(stream)
		send := func(msg tea.Msg) {
			select {
			case stream <- msg:
			case <-m.ctx.Done():
			}
		}
		report, err := m.clusterRun(m.ctx, opts, task, func(ev cluster.Event) {
			send(clusterEventMsg{Event: ev})
		})
		if err != nil {
			send(streamDoneMsg{Answer: "", Err: err})
			return nil
		}
		stats := report.Stats
		answer := fmt.Sprintf("%s\n\nagents: %d · succeeded: %d · failed: %d · peak concurrency: %d · total: %s\nsandboxes: %s",
			report.Summary, stats.Total, stats.Succeeded, stats.Failed, stats.PeakConcurrency, stats.Duration.Round(time.Millisecond), report.RunDir)
		send(streamDoneMsg{Answer: answer, Err: nil})
		return nil
	}
}

// applyClusterEvent folds a cluster event into the progress panel state.
func (m *model) applyClusterEvent(ev cluster.Event) {
	p := m.cluster
	if p == nil {
		return
	}
	switch ev.Type {
	case "start":
		p.running++
	case "done":
		p.running--
		p.done++
		p.pushRecent(clusterDoneStyle.Render(fmt.Sprintf("#%d ✓", ev.Worker+1)) + " " + mutedStyle.Render(ev.Content))
	case "error":
		p.running--
		p.failed++
		p.pushRecent(clusterFailedStyle.Render(fmt.Sprintf("#%d ✗", ev.Worker+1)) + " " + mutedStyle.Render(truncateMiddle(ev.Content, 60)))
	case "plan":
		p.pushRecent(mutedStyle.Render(fmt.Sprintf("#%d", ev.Worker+1)) + " " + ev.Content)
	case "info":
		p.pushRecent(mutedStyle.Render("· " + ev.Content))
	}
}

func (p *clusterPanel) pushRecent(line string) {
	if strings.TrimSpace(line) == "" {
		return
	}
	p.recent = append(p.recent, line)
	if len(p.recent) > clusterRecentLines {
		p.recent = p.recent[len(p.recent)-clusterRecentLines:]
	}
}

func (m *model) finishCluster() {
	m.cluster = nil
}

// renderClusterPanel draws the live progress panel appended below the
// transcript while a cluster run is active.
func (m model) renderClusterPanel() string {
	p := m.cluster
	if p == nil {
		return ""
	}
	width := max(40, m.width-6)
	barWidth := max(10, width-4)
	snapshot := cluster.Progress{
		Total:   p.total,
		Done:    p.done,
		Failed:  p.failed,
		Running: p.running,
		Elapsed: time.Since(p.started),
	}

	var b strings.Builder
	b.WriteString(brandStyle.Render("◆ Agent Cluster") + " " + mutedStyle.Render(truncateMiddle(p.task, max(10, width-20))) + "\n")
	b.WriteString(m.renderClusterBar(snapshot, barWidth) + "\n")
	stats := clusterDoneStyle.Render(fmt.Sprintf("✓ %d", p.done)) + faintStyle.Render("/") + mutedStyle.Render(fmt.Sprintf("%d", p.total))
	stats += faintStyle.Render("  ·  ") + clusterFailedStyle.Render(fmt.Sprintf("✗ %d", p.failed))
	stats += faintStyle.Render("  ·  ") + clusterRunningStyle.Render(fmt.Sprintf("▶ %d", p.running))
	stats += faintStyle.Render("  ·  ") + mutedStyle.Render(snapshot.Elapsed.Round(time.Second).String())
	b.WriteString(stats)
	if len(p.recent) > 0 {
		b.WriteString("\n\n" + strings.Join(p.recent, "\n"))
	}
	return clusterPanelStyle.Width(width).Render(b.String())
}

// renderClusterBar colors the proportional bar segments: green for done,
// red for failed, yellow for running, dark for remaining.
func (m model) renderClusterBar(p cluster.Progress, width int) string {
	done, failed, running, empty := p.Segments(width)
	return clusterDoneStyle.Render(strings.Repeat("█", done)) +
		clusterFailedStyle.Render(strings.Repeat("█", failed)) +
		clusterRunningStyle.Render(strings.Repeat("▒", running)) +
		clusterEmptyStyle.Render(strings.Repeat("░", empty))
}
