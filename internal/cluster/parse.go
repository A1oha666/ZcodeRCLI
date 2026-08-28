package cluster

import (
	"fmt"
	"strconv"
	"strings"
)

// CommandOptions holds the flags accepted by the /cluster slash command and
// the zcoder cluster subcommand.
type CommandOptions struct {
	Agents      int
	Concurrency int
	Simulate    bool
}

// ParseClusterCommand splits a "/cluster [--agents N] [--concurrency N]
// [--simulate] task text" command into options and the remaining task. The
// leading "/cluster" prefix is optional.
func ParseClusterCommand(text string) (CommandOptions, string, error) {
	text = strings.TrimSpace(text)
	if strings.HasPrefix(text, "/cluster") {
		text = strings.TrimPrefix(text, "/cluster")
	}
	text = strings.TrimSpace(text)
	opts := CommandOptions{Agents: 8, Concurrency: 8}
	taskParts := make([]string, 0, 8)
	for text != "" {
		field := text
		if idx := strings.IndexAny(text, " \t"); idx >= 0 {
			field, text = text[:idx], strings.TrimLeft(text[idx:], " \t")
		} else {
			text = ""
		}
		switch field {
		case "--simulate":
			opts.Simulate = true
		case "--agents", "--concurrency":
			value, rest, err := nextIntValue(text)
			if err != nil {
				return opts, "", fmt.Errorf("/cluster %s: %w", field, err)
			}
			if field == "--agents" {
				opts.Agents = value
			} else {
				opts.Concurrency = value
			}
			text = rest
		default:
			taskParts = append(taskParts, field)
		}
	}
	task := strings.TrimSpace(strings.Join(taskParts, " "))
	if task == "" {
		return opts, "", fmt.Errorf("/cluster requires a task")
	}
	return opts, task, nil
}

func nextIntValue(text string) (int, string, error) {
	field := text
	rest := ""
	if idx := strings.IndexAny(text, " \t"); idx >= 0 {
		field, rest = text[:idx], strings.TrimLeft(text[idx:], " \t")
	}
	value, err := strconv.Atoi(field)
	if err != nil || value < 1 {
		return 0, "", fmt.Errorf("expected a positive integer, got %q", field)
	}
	return value, rest, nil
}
