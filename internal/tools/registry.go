package tools

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bmatcuk/doublestar/v4"

	"github.com/itwanger/zcoder-go/internal/config"
	"github.com/itwanger/zcoder-go/internal/llm"
	"github.com/itwanger/zcoder-go/internal/rag"
	"github.com/itwanger/zcoder-go/internal/skill"
	"github.com/itwanger/zcoder-go/internal/snapshot"
)

type Registry struct {
	root          string
	pathGuard     PathGuard
	tools         map[string]Tool
	cfg           config.Config
	rag           *rag.Index
	skills        *skill.Registry
	memorySaver   func(content, scope string) error
	snapshot      *snapshot.Service
	postWriteHook func(context.Context, string) string
	audit         AuditLog
	mcpClosers    []io.Closer
	mu            sync.RWMutex
}

type Options struct {
	Config        config.Config
	RAG           *rag.Index
	Skills        *skill.Registry
	MemorySaver   func(content, scope string) error
	Snapshot      *snapshot.Service
	PostWriteHook func(context.Context, string) string
}

type Tool struct {
	Name        string
	Description string
	Parameters  map[string]any
	Executor    Executor
	Dangerous   bool
}

type Executor func(ctx context.Context, args map[string]any) (string, error)

type Result struct {
	ID      string
	Name    string
	Content string
	Error   error
}

func NewRegistry(root string, opts Options) *Registry {
	abs, _ := filepath.Abs(root)
	r := &Registry{
		root:          abs,
		pathGuard:     PathGuard{Root: abs},
		tools:         map[string]Tool{},
		cfg:           opts.Config,
		rag:           opts.RAG,
		skills:        opts.Skills,
		memorySaver:   opts.MemorySaver,
		snapshot:      opts.Snapshot,
		postWriteHook: opts.PostWriteHook,
		audit:         AuditLog{Dir: filepath.Join(config.HomeDir(), ".zcoder", "audit")},
	}
	r.registerBuiltins()
	return r
}

func (r *Registry) Close() {
	for _, closer := range r.mcpClosers {
		_ = closer.Close()
	}
}

func (r *Registry) Definitions() []llm.Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.tools))
	for name := range r.tools {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]llm.Tool, 0, len(names))
	for _, name := range names {
		t := r.tools[name]
		out = append(out, llm.Tool{Name: t.Name, Description: t.Description, Parameters: t.Parameters})
	}
	return out
}

func (r *Registry) ExecuteAll(ctx context.Context, calls []llm.ToolCall) []Result {
	results := make([]Result, len(calls))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	for i, call := range calls {
		wg.Add(1)
		go func(i int, call llm.ToolCall) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			cctx, cancel := context.WithTimeout(ctx, 90*time.Second)
			defer cancel()
			results[i] = r.Execute(cctx, call)
		}(i, call)
	}
	wg.Wait()
	return results
}

func (r *Registry) Execute(ctx context.Context, call llm.ToolCall) Result {
	name := call.Function.Name
	var args map[string]any
	if len(call.Function.Arguments) > 0 {
		if err := json.Unmarshal(call.Function.Arguments, &args); err != nil {
			return Result{ID: call.ID, Name: name, Content: "invalid tool arguments: " + err.Error(), Error: err}
		}
	}
	r.mu.RLock()
	tool, ok := r.tools[name]
	r.mu.RUnlock()
	if !ok {
		err := fmt.Errorf("unknown tool %s", name)
		return Result{ID: call.ID, Name: name, Content: err.Error(), Error: err}
	}
	start := time.Now()
	content, err := tool.Executor(ctx, args)
	outcome := "allow"
	if err != nil {
		outcome = "error"
		content = err.Error()
	}
	if tool.Dangerous || strings.HasPrefix(name, "mcp__") {
		_ = r.audit.Write(AuditEntry{
			Time:     time.Now(),
			Tool:     name,
			Outcome:  outcome,
			Approver: "policy",
			ArgsHash: hashArgs(args),
			Duration: time.Since(start).String(),
		})
	}
	return Result{ID: call.ID, Name: name, Content: content, Error: err}
}

func (r *Registry) register(tool Tool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tools[tool.Name] = tool
}

func (r *Registry) registerBuiltins() {
	r.registerFileTools()
	r.registerShellTools()
	r.registerRAGTools()
	r.registerWebTools()
	r.registerMemorySkillTools()
	r.registerSnapshotTools()
	r.registerBrowserTools()
}

func (r *Registry) registerFileTools() {
	r.register(Tool{
		Name:        "read_file",
		Description: "Read a UTF-8 text file inside the workspace. Supports offset and limit by line number.",
		Parameters:  objectParams(required("path", "string", "file path"), optional("offset", "integer", "1-based start line"), optional("limit", "integer", "max lines")),
		Executor: func(ctx context.Context, args map[string]any) (string, error) {
			path, err := r.pathGuard.Resolve(strArg(args, "path"))
			if err != nil {
				return "", err
			}
			offset := intArg(args, "offset", 1)
			limit := intArg(args, "limit", 2000)
			return readFileLines(path, offset, limit)
		},
	})
	r.register(Tool{
		Name:        "write_file",
		Description: "Write a UTF-8 text file inside the workspace. Single write is capped at 5MB.",
		Parameters:  objectParams(required("path", "string", "file path"), required("content", "string", "file content")),
		Dangerous:   true,
		Executor: func(ctx context.Context, args map[string]any) (string, error) {
			path, err := r.pathGuard.Resolve(strArg(args, "path"))
			if err != nil {
				return "", err
			}
			content := strArg(args, "content")
			if len([]byte(content)) > 5*1024*1024 {
				return "", fmt.Errorf("write_file rejected: content exceeds 5MB")
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return "", err
			}
			if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
				return "", err
			}
			msg := "wrote " + rel(r.root, path)
			if r.postWriteHook != nil {
				if diag := r.postWriteHook(ctx, path); strings.TrimSpace(diag) != "" {
					msg += "\n\nDiagnostics:\n" + diag
				}
			}
			return msg, nil
		},
	})
	r.register(Tool{
		Name:        "list_dir",
		Description: "List files and directories inside the workspace.",
		Parameters:  objectParams(optional("path", "string", "directory path")),
		Executor: func(ctx context.Context, args map[string]any) (string, error) {
			p := strArg(args, "path")
			if p == "" {
				p = "."
			}
			path, err := r.pathGuard.Resolve(p)
			if err != nil {
				return "", err
			}
			entries, err := os.ReadDir(path)
			if err != nil {
				return "", err
			}
			var b strings.Builder
			for _, e := range entries {
				suffix := ""
				if e.IsDir() {
					suffix = "/"
				}
				b.WriteString(e.Name() + suffix + "\n")
			}
			return strings.TrimRight(b.String(), "\n"), nil
		},
	})
	r.register(Tool{
		Name:        "glob_files",
		Description: "Glob files inside the workspace. Supports ** patterns.",
		Parameters:  objectParams(required("pattern", "string", "glob pattern")),
		Executor: func(ctx context.Context, args map[string]any) (string, error) {
			pattern := filepath.ToSlash(strArg(args, "pattern"))
			matches, err := doublestar.FilepathGlob(filepath.Join(r.root, pattern))
			if err != nil {
				return "", err
			}
			out := make([]string, 0, len(matches))
			for _, m := range matches {
				if ignoredPath(m) {
					continue
				}
				out = append(out, rel(r.root, m))
			}
			sort.Strings(out)
			if len(out) > 500 {
				out = out[:500]
				out = append(out, "... truncated at 500 matches")
			}
			return strings.Join(out, "\n"), nil
		},
	})
	r.register(Tool{
		Name:        "grep_code",
		Description: "Search code text. Prefer this for exact symbol, string, error and filename discovery.",
		Parameters:  objectParams(required("pattern", "string", "regex or literal pattern"), optional("path", "string", "subdirectory"), optional("max_results", "integer", "result cap")),
		Executor: func(ctx context.Context, args map[string]any) (string, error) {
			p := strArg(args, "path")
			if p == "" {
				p = "."
			}
			root, err := r.pathGuard.Resolve(p)
			if err != nil {
				return "", err
			}
			return grep(ctx, r.root, root, strArg(args, "pattern"), intArg(args, "max_results", 200))
		},
	})
	r.register(Tool{
		Name:        "create_project",
		Description: "Create a project directory inside the workspace with a README.",
		Parameters:  objectParams(required("name", "string", "project directory name"), optional("description", "string", "README description")),
		Dangerous:   true,
		Executor: func(ctx context.Context, args map[string]any) (string, error) {
			name := filepath.Clean(strArg(args, "name"))
			path, err := r.pathGuard.Resolve(name)
			if err != nil {
				return "", err
			}
			if err := os.MkdirAll(path, 0o755); err != nil {
				return "", err
			}
			desc := strArg(args, "description")
			if desc == "" {
				desc = "Generated by Zcoder Go."
			}
			if err := os.WriteFile(filepath.Join(path, "README.md"), []byte("# "+filepath.Base(path)+"\n\n"+desc+"\n"), 0o644); err != nil {
				return "", err
			}
			return "created project " + rel(r.root, path), nil
		},
	})
}

func (r *Registry) registerShellTools() {
	r.register(Tool{
		Name:        "execute_command",
		Description: "Execute a shell command in the workspace. Dangerous commands are rejected before execution.",
		Parameters:  objectParams(required("command", "string", "shell command"), optional("timeout_seconds", "integer", "timeout seconds")),
		Dangerous:   true,
		Executor: func(ctx context.Context, args map[string]any) (string, error) {
			command := strArg(args, "command")
			if err := CommandGuard(command); err != nil {
				return "", err
			}
			timeout := time.Duration(intArg(args, "timeout_seconds", 60)) * time.Second
			cctx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			cmd := exec.CommandContext(cctx, "sh", "-lc", command)
			cmd.Dir = r.root
			var out bytes.Buffer
			cmd.Stdout = &out
			cmd.Stderr = &out
			err := cmd.Run()
			text := out.String()
			if len(text) > 12000 {
				text = text[:12000] + "\n... output truncated"
			}
			if cctx.Err() == context.DeadlineExceeded {
				return text, fmt.Errorf("command timed out after %s", timeout)
			}
			if err != nil {
				return text, fmt.Errorf("command failed: %w\n%s", err, text)
			}
			if strings.TrimSpace(text) == "" {
				text = "(command completed with no output)"
			}
			return text, nil
		},
	})
}

func (r *Registry) registerRAGTools() {
	r.register(Tool{
		Name:        "search_code",
		Description: "Semantic/lexical auxiliary search over the code index. Use grep_code/read_file for exact localization first.",
		Parameters:  objectParams(required("query", "string", "search query"), optional("top_k", "integer", "number of results")),
		Executor: func(ctx context.Context, args map[string]any) (string, error) {
			if r.rag == nil {
				return "", fmt.Errorf("RAG index is not configured")
			}
			results := r.rag.Search(strArg(args, "query"), intArg(args, "top_k", 8))
			if len(results) == 0 {
				return "no indexed matches; run `zcoder index` first or use grep_code", nil
			}
			var b strings.Builder
			for _, item := range results {
				b.WriteString(fmt.Sprintf("%s:%d score=%.3f symbol=%s\n%s\n\n",
					item.Chunk.Path, item.Chunk.StartLine, item.Score, item.Chunk.Symbol, item.Chunk.Preview()))
			}
			return strings.TrimSpace(b.String()), nil
		},
	})
}

func (r *Registry) registerWebTools() {
	r.register(Tool{
		Name:        "web_search",
		Description: "Search the web using SerpAPI, SearXNG, or the built-in DuckDuckGo fallback.",
		Parameters:  objectParams(required("query", "string", "search query"), optional("num_results", "integer", "result count")),
		Executor: func(ctx context.Context, args map[string]any) (string, error) {
			return r.webSearch(ctx, strArg(args, "query"), intArg(args, "num_results", 5))
		},
	})
	r.register(Tool{
		Name:        "web_fetch",
		Description: "Fetch a public URL and extract readable text. Blocks file, loopback and private-network URLs.",
		Parameters:  objectParams(required("url", "string", "URL to fetch"), optional("max_chars", "integer", "max output chars")),
		Executor: func(ctx context.Context, args map[string]any) (string, error) {
			return webFetch(ctx, strArg(args, "url"), intArg(args, "max_chars", 8000))
		},
	})
}

func (r *Registry) registerMemorySkillTools() {
	r.register(Tool{
		Name:        "save_memory",
		Description: "Save a stable fact only when the user explicitly asks Zcoder to remember it.",
		Parameters:  objectParams(required("content", "string", "stable fact"), optional("scope", "string", "project or global")),
		Executor: func(ctx context.Context, args map[string]any) (string, error) {
			if r.memorySaver == nil {
				return "", fmt.Errorf("memory store is not configured")
			}
			scope := strArg(args, "scope")
			if scope == "" {
				scope = "project"
			}
			if err := r.memorySaver(strArg(args, "content"), scope); err != nil {
				return "", err
			}
			return "memory saved (" + scope + ")", nil
		},
	})
	r.register(Tool{
		Name:        "load_skill",
		Description: "Load full SKILL.md instructions for an indexed relevant skill. Pass the exact kebab-case skill name.",
		Parameters:  objectParams(required("name", "string", "skill name")),
		Executor: func(ctx context.Context, args map[string]any) (string, error) {
			if r.skills == nil {
				return "", fmt.Errorf("skill registry is not configured")
			}
			return r.skills.Load(strArg(args, "name"))
		},
	})
}

func (r *Registry) registerSnapshotTools() {
	r.register(Tool{
		Name:        "revert_turn",
		Description: "Restore a previously created workspace snapshot by id. Dangerous and audited.",
		Parameters:  objectParams(required("id", "string", "snapshot id")),
		Dangerous:   true,
		Executor: func(ctx context.Context, args map[string]any) (string, error) {
			if r.snapshot == nil {
				return "", fmt.Errorf("snapshot service is not configured")
			}
			if err := r.snapshot.Restore(strArg(args, "id")); err != nil {
				return "", err
			}
			return "restored snapshot " + strArg(args, "id"), nil
		},
	})
}

func (r *Registry) registerBrowserTools() {
	r.register(Tool{Name: "browser_status", Description: "Report browser MCP status and recommended setup.", Parameters: objectParams(), Executor: func(ctx context.Context, args map[string]any) (string, error) {
		return "Browser automation is available through configured MCP tools such as chrome-devtools. Use /mcp config with chrome-devtools-mcp for shared or isolated sessions.", nil
	}})
	r.register(Tool{Name: "browser_connect", Description: "Explain how to switch chrome-devtools MCP to a shared browser session.", Parameters: objectParams(optional("port", "integer", "CDP port")), Executor: func(ctx context.Context, args map[string]any) (string, error) {
		return "Configure chrome-devtools MCP with --autoConnect or --browser-url=http://127.0.0.1:9222, then restart MCP. Sensitive write actions remain audited.", nil
	}})
	r.register(Tool{Name: "browser_disconnect", Description: "Explain how to return browser MCP to isolated mode.", Parameters: objectParams(), Executor: func(ctx context.Context, args map[string]any) (string, error) {
		return "Configure chrome-devtools MCP with --isolated=true and restart MCP to return to isolated mode.", nil
	}})
}

func objectParams(fields ...map[string]any) map[string]any {
	props := map[string]any{}
	req := []string{}
	for _, f := range fields {
		name := f["name"].(string)
		requiredFlag := f["required"].(bool)
		delete(f, "name")
		delete(f, "required")
		props[name] = f
		if requiredFlag {
			req = append(req, name)
		}
	}
	return map[string]any{"type": "object", "properties": props, "required": req}
}

func required(name, typ, desc string) map[string]any {
	return map[string]any{"name": name, "type": typ, "description": desc, "required": true}
}

func optional(name, typ, desc string) map[string]any {
	return map[string]any{"name": name, "type": typ, "description": desc, "required": false}
}

func strArg(args map[string]any, key string) string {
	v, ok := args[key]
	if !ok || v == nil {
		return ""
	}
	switch x := v.(type) {
	case string:
		return x
	default:
		return fmt.Sprint(x)
	}
}

func intArg(args map[string]any, key string, def int) int {
	v, ok := args[key]
	if !ok || v == nil {
		return def
	}
	switch x := v.(type) {
	case int:
		return x
	case float64:
		return int(x)
	case string:
		n, err := strconv.Atoi(x)
		if err == nil {
			return n
		}
	}
	return def
}

func readFileLines(path string, offset, limit int) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	all := strings.Split(string(b), "\n")
	if offset < 1 {
		offset = 1
	}
	if limit <= 0 || limit > 2000 {
		limit = 2000
	}
	start := offset - 1
	if start >= len(all) {
		return "", nil
	}
	end := start + limit
	if end > len(all) {
		end = len(all)
	}
	var out strings.Builder
	for i := start; i < end; i++ {
		out.WriteString(fmt.Sprintf("%5d  %s\n", i+1, all[i]))
	}
	if end < len(all) {
		out.WriteString(fmt.Sprintf("... truncated; next offset %d\n", end+1))
	}
	return out.String(), nil
}

func grep(ctx context.Context, projectRoot, root, pattern string, max int) (string, error) {
	if max <= 0 || max > 500 {
		max = 200
	}
	if rg, err := exec.LookPath("rg"); err == nil {
		args := []string{"-n", "--color", "never", "--hidden", "--glob", "!.git", "--glob", "!node_modules", "--glob", "!target", "--glob", "!dist", pattern, root}
		cmd := exec.CommandContext(ctx, rg, args...)
		out, err := cmd.CombinedOutput()
		text := string(out)
		lines := strings.Split(strings.TrimSpace(text), "\n")
		if len(lines) > max {
			lines = append(lines[:max], "... truncated")
		}
		for i, line := range lines {
			lines[i] = strings.Replace(line, projectRoot+string(os.PathSeparator), "", 1)
		}
		if len(lines) == 1 && lines[0] == "" {
			return "no matches", nil
		}
		if err != nil && len(text) == 0 {
			return "no matches", nil
		}
		return strings.Join(lines, "\n"), nil
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return "", err
	}
	var matches []string
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || len(matches) >= max {
			return nil
		}
		if d.IsDir() {
			if ignoredPath(path) {
				return filepath.SkipDir
			}
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		for i, line := range strings.Split(string(b), "\n") {
			if re.MatchString(line) {
				matches = append(matches, fmt.Sprintf("%s:%d:%s", rel(projectRoot, path), i+1, line))
				if len(matches) >= max {
					break
				}
			}
		}
		return nil
	})
	if len(matches) == 0 {
		return "no matches", nil
	}
	return strings.Join(matches, "\n"), nil
}

func ignoredPath(path string) bool {
	base := filepath.Base(path)
	switch base {
	case ".git", ".zcoder", "node_modules", "target", "dist", "build", "coverage", "vendor":
		return true
	default:
		return false
	}
}

func rel(root, path string) string {
	r, err := filepath.Rel(root, path)
	if err != nil {
		return path
	}
	return filepath.ToSlash(r)
}

func hashArgs(args map[string]any) string {
	b, _ := json.Marshal(args)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])[:16]
}
