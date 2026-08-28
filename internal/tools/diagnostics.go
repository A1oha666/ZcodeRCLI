package tools

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func GoDiagnosticsHook(project string) func(context.Context, string) string {
	return func(ctx context.Context, path string) string {
		if filepath.Ext(path) != ".go" {
			return ""
		}
		cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		cmd := exec.CommandContext(cctx, "go", "test", "./...")
		cmd.Dir = project
		out, err := cmd.CombinedOutput()
		if err == nil {
			return ""
		}
		text := strings.TrimSpace(string(out))
		if len(text) > 4000 {
			text = text[:4000] + "\n... diagnostics truncated"
		}
		return text
	}
}
