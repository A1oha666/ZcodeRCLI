package cluster

import (
	"fmt"
	"strings"
	"time"
)

// Progress is a snapshot of cluster state for rendering. Both the plain CLI
// line and the TUI panel render from this type.
type Progress struct {
	Total   int
	Done    int
	Failed  int
	Running int
	Elapsed time.Duration
}

// Finished reports whether every worker has completed (successfully or not).
func (p Progress) Finished() bool {
	return p.Done+p.Failed >= p.Total && p.Total > 0
}

// Segments splits a bar of the given width into proportional segments:
// done, failed, running and empty cell counts that sum to width.
func (p Progress) Segments(width int) (done, failed, running, empty int) {
	if width <= 0 {
		return 0, 0, 0, 0
	}
	if p.Total <= 0 {
		return 0, 0, 0, width
	}
	accounted := p.Done + p.Failed + p.Running
	if accounted > p.Total {
		// Defensive: never let the bar overflow if counters race.
		p.Done, p.Failed, p.Running = capShare(p.Done, accounted, p.Total),
			capShare(p.Failed, accounted, p.Total), capShare(p.Running, accounted, p.Total)
		accounted = p.Total
	}
	done = width * p.Done / p.Total
	failed = width * p.Failed / p.Total
	running = width * p.Running / p.Total
	if rest := width - done - failed - running; rest > 0 {
		if accounted >= p.Total {
			// All workers accounted for: give leftovers to done so the bar
			// reaches full width at 100%.
			done += rest
		} else if accounted > 0 {
			// Partially through the run: show a bit of forward motion.
			done += rest / 3
			empty = rest - rest/3
		} else {
			empty = rest
		}
	}
	return done, failed, running, empty
}

func capShare(value, accounted, total int) int {
	if accounted <= 0 {
		return 0
	}
	return value * total / accounted
}

// RenderBar renders a plain single-line progress string without colors:
//
//	████████░░░░░░ 42/100 done · 2 fail · 8 run · 12s
func (p Progress) RenderBar(width int) string {
	done, failed, running, empty := p.Segments(width)
	bar := strings.Repeat("█", done) +
		strings.Repeat("█", failed) +
		strings.Repeat("▒", running) +
		strings.Repeat("░", empty)
	line := fmt.Sprintf("%s %d/%d done", bar, p.Done+p.Failed, p.Total)
	if p.Failed > 0 {
		line += fmt.Sprintf(" · %d fail", p.Failed)
	}
	if p.Running > 0 {
		line += fmt.Sprintf(" · %d run", p.Running)
	}
	if p.Elapsed > 0 {
		line += fmt.Sprintf(" · %s", p.Elapsed.Round(time.Second))
	}
	return line
}
