package cluster

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/itwanger/zcoder-go/internal/config"
)

// worktreePool provisions one git worktree per worker so agents can edit
// files concurrently without stepping on each other. Creation is serialized
// because concurrent `git worktree add` calls race on .git/worktrees.
type worktreePool struct {
	root    string
	mu      sync.Mutex
	created []string
}

func newWorktreePool(root string) *worktreePool {
	return &worktreePool{root: root}
}

// IsGitRepo reports whether root is inside a git working tree.
func IsGitRepo(root string) bool {
	return git(root, "rev-parse", "--is-inside-work-tree") == "true"
}

// create checks out a detached-HEAD worktree at path and remembers it for
// later cleanup.
func (p *worktreePool) create(path string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if out := gitRun(p.root, "worktree", "add", "--detach", path, "HEAD"); out != "" {
		return fmt.Errorf("git worktree add %s: %s", path, out)
	}
	p.created = append(p.created, path)
	return nil
}

// remove deletes one worktree (best effort).
func (p *worktreePool) remove(path string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if out := gitRun(p.root, "worktree", "remove", "--force", path); out != "" {
		return fmt.Errorf("git worktree remove %s: %s", path, out)
	}
	return nil
}

// removeAll tears down every worktree this pool created (best effort) and
// prunes stale registrations.
func (p *worktreePool) removeAll() {
	p.mu.Lock()
	paths := append([]string(nil), p.created...)
	p.created = nil
	p.mu.Unlock()
	for _, path := range paths {
		_ = p.remove(path)
	}
	_ = gitRun(p.root, "worktree", "prune")
}

func git(root string, args ...string) string {
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// gitRun runs a mutating git command and returns combined stderr/stdout text
// on failure (" on success).
func gitRun(root string, args ...string) string {
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return strings.TrimSpace(string(out))
	}
	return ""
}

func homeClusterDir() string {
	return filepath.Join(config.HomeDir(), ".zcoder")
}

func sandboxBaseFor(isolateWorktrees bool, projectRoot, workspaceDir string) string {
	if isolateWorktrees && workspaceDir == "" {
		// Keep worktrees outside the repository so they never pollute
		// `git status` in the user's project.
		return filepath.Join(homeClusterDir(), "cluster")
	}
	if workspaceDir != "" {
		return workspaceDir
	}
	return filepath.Join(projectRoot, ".zcoder", "cluster")
}
