package tools

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

type PathGuard struct {
	Root string
}

func (g PathGuard) Resolve(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		path = "."
	}
	if strings.HasPrefix(path, "~") {
		return "", fmt.Errorf("path %q is outside workspace; ~ expansion is not allowed", path)
	}
	candidate := path
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(g.Root, candidate)
	}
	clean, err := filepath.Abs(candidate)
	if err != nil {
		return "", err
	}
	rootReal, err := filepath.EvalSymlinks(g.Root)
	if err != nil {
		rootReal = g.Root
	}
	parent := clean
	if _, err := os.Stat(clean); err != nil {
		parent = filepath.Dir(clean)
	}
	parentReal, err := filepath.EvalSymlinks(parent)
	if err != nil {
		parentReal = parent
	}
	if !within(rootReal, parentReal) && !within(g.Root, clean) {
		return "", fmt.Errorf("path %q escapes workspace %s", path, g.Root)
	}
	return clean, nil
}

func within(root, path string) bool {
	root = filepath.Clean(root)
	path = filepath.Clean(path)
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}

var dangerousCommands = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\bsudo\b`),
	regexp.MustCompile(`(?i)\brm\s+-[^\n]*r[^\n]*f\s+(/|\$HOME|~|\*)`),
	regexp.MustCompile(`(?i)\bmkfs\b`),
	regexp.MustCompile(`(?i)\bdd\s+.*\bof=/dev/`),
	regexp.MustCompile(`:\(\)\s*\{\s*:\|:`),
	regexp.MustCompile(`(?i)(curl|wget)\b[^\n|;]*\|\s*(sh|bash)`),
	regexp.MustCompile(`(?i)\bshutdown\b|\breboot\b`),
	regexp.MustCompile(`(?i)\bchmod\s+777\s+/`),
	regexp.MustCompile(`(?i)\bfind\s+/(\s|$)`),
}

func CommandGuard(command string) error {
	for _, re := range dangerousCommands {
		if re.MatchString(command) {
			return fmt.Errorf("command rejected by policy: %s", re.String())
		}
	}
	return nil
}

type AuditLog struct {
	Dir string
}

type AuditEntry struct {
	Time     time.Time `json:"time"`
	Tool     string    `json:"tool"`
	Outcome  string    `json:"outcome"`
	Approver string    `json:"approver"`
	ArgsHash string    `json:"args_hash"`
	Duration string    `json:"duration"`
}

func (a AuditLog) Write(entry AuditEntry) error {
	if err := os.MkdirAll(a.Dir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(a.Dir, entry.Time.Format("2006-01-02")+".jsonl")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	b, _ := json.Marshal(entry)
	_, err = f.Write(append(b, '\n'))
	return err
}
