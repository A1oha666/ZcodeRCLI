package cluster

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// initTempRepo creates a git repo with one commit so worktrees can branch off HEAD.
func initTempRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	run("init", "-b", "main")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "test")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("demo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-m", "init")
	return dir
}

func TestWorktreeCreateWriteRemove(t *testing.T) {
	repo := initTempRepo(t)
	pool := newWorktreePool(repo)
	base := t.TempDir()
	path := filepath.Join(base, "worker-0001")

	if err := pool.create(path); err != nil {
		t.Fatalf("create worktree: %v", err)
	}
	// The worktree is a real checkout of HEAD.
	if _, err := os.Stat(filepath.Join(path, "README.md")); err != nil {
		t.Fatalf("expected checked out README.md: %v", err)
	}
	// A worker can write inside it.
	if err := os.WriteFile(filepath.Join(path, "a.md"), []byte("hi"), 0o644); err != nil {
		t.Fatalf("write in worktree: %v", err)
	}
	if err := pool.remove(path); err != nil {
		t.Fatalf("remove worktree: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("worktree dir should be gone, got %v", err)
	}
}

func TestClusterWorktreeIsolationEndToEnd(t *testing.T) {
	repo := initTempRepo(t)
	cl := New(Config{
		Agents:       2,
		Concurrency:  2,
		ProjectRoot:  repo,
		WorkspaceDir: t.TempDir(),
		Isolation:    "worktree",
	}, &stubClient{}, nil)

	report, err := cl.Run(context.Background(), "demo task")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if report.Stats.Succeeded != 2 {
		t.Fatalf("expected 2 successes, got %+v", report.Stats)
	}
	// Worktrees are removed after the run by default.
	entries, err := os.ReadDir(report.RunDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected worktrees to be cleaned up, found %d entries", len(entries))
	}
}

func TestClusterKeepWorktrees(t *testing.T) {
	repo := initTempRepo(t)
	cl := New(Config{
		Agents:        1,
		Concurrency:   1,
		ProjectRoot:   repo,
		WorkspaceDir:  t.TempDir(),
		Isolation:     "worktree",
		KeepWorktrees: true,
	}, &stubClient{}, nil)

	report, err := cl.Run(context.Background(), "demo task")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	entries, err := os.ReadDir(report.RunDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected the kept worktree, found %d entries", len(entries))
	}
}

func TestClusterAutoIsolationFallsBackToDir(t *testing.T) {
	plain := t.TempDir() // not a git repo
	cl := New(Config{
		Agents:       1,
		Concurrency:  1,
		ProjectRoot:  plain,
		WorkspaceDir: plain,
		Isolation:    "worktree", // explicitly requested but impossible
	}, &stubClient{}, nil)

	report, err := cl.Run(context.Background(), "demo task")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if report.Stats.Succeeded != 1 {
		t.Fatalf("expected dir-sandbox fallback to succeed, got %+v", report.Stats)
	}
	if _, err := os.Stat(filepath.Join(report.RunDir, "worker-0001")); err != nil {
		t.Fatalf("expected dir sandbox: %v", err)
	}
}

func TestProgressSegments(t *testing.T) {
	p := Progress{Total: 100, Done: 50, Failed: 10, Running: 40}
	done, failed, running, empty := p.Segments(40)
	if done+failed+running+empty != 40 {
		t.Fatalf("segments must sum to width: %d", done+failed+running+empty)
	}
	if done != 20 || failed != 4 || running != 16 {
		t.Fatalf("unexpected segments: done=%d failed=%d running=%d empty=%d", done, failed, running, empty)
	}

	full := Progress{Total: 10, Done: 8, Failed: 2}
	d, f, r, e := full.Segments(10)
	if d+f+r+e != 10 || r != 0 || e != 0 {
		t.Fatalf("finished bar should be full: %d/%d/%d/%d", d, f, r, e)
	}

	zero := Progress{}
	if _, _, _, e := zero.Segments(10); e != 10 {
		t.Fatalf("empty progress should be all empty cells, got empty=%d", e)
	}
}

func TestRenderBar(t *testing.T) {
	p := Progress{Total: 4, Done: 2, Failed: 1, Running: 1}
	bar := p.RenderBar(8)
	if len(bar) == 0 {
		t.Fatal("expected non-empty bar")
	}
	for _, glyph := range []string{"█", "▒"} {
		if !containsRune(bar, glyph) {
			t.Fatalf("bar %q missing glyph %q", bar, glyph)
		}
	}
	if !containsRune(bar, "3/4") {
		t.Fatalf("bar %q missing progress count", bar)
	}
	// A partially-accounted run shows empty cells for the remaining workers.
	partial := Progress{Total: 4, Done: 1, Running: 1}
	if !containsRune(partial.RenderBar(8), "░") {
		t.Fatalf("partial bar %q should contain empty cells", partial.RenderBar(8))
	}
}

func containsRune(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func TestParseClusterCommand(t *testing.T) {
	opts, task, err := ParseClusterCommand("/cluster --agents 20 --concurrency 5 --simulate write tests")
	if err != nil {
		t.Fatal(err)
	}
	if opts.Agents != 20 || opts.Concurrency != 5 || !opts.Simulate {
		t.Fatalf("unexpected opts: %+v", opts)
	}
	if task != "write tests" {
		t.Fatalf("unexpected task %q", task)
	}

	opts, task, err = ParseClusterCommand("plain task without prefix")
	if err != nil {
		t.Fatal(err)
	}
	if opts.Agents != 8 || task != "plain task without prefix" {
		t.Fatalf("unexpected defaults: %+v %q", opts, task)
	}

	if _, _, err := ParseClusterCommand("/cluster"); err == nil {
		t.Fatal("expected error for missing task")
	}
	if _, _, err := ParseClusterCommand("/cluster --agents abc task"); err == nil {
		t.Fatal("expected error for non-integer --agents")
	}
}
