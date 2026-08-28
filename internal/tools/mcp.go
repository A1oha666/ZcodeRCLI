package tools

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type mcpConfigFile struct {
	MCPServers map[string]mcpServerConfig `json:"mcpServers"`
}

type mcpServerConfig struct {
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
	Env     map[string]string `json:"env"`
}

type mcpClient interface {
	Call(ctx context.Context, method string, params any) (json.RawMessage, error)
	Close() error
}

func (r *Registry) LoadMCP(ctx context.Context) (ready int, toolCount int) {
	servers := r.loadMCPConfig()
	for name, cfg := range servers {
		client, err := r.startMCPServer(ctx, name, cfg)
		if err != nil {
			continue
		}
		ready++
		r.mcpClosers = append(r.mcpClosers, client)
		if err := r.initializeMCP(ctx, client); err != nil {
			continue
		}
		n := r.registerMCPTools(ctx, name, client)
		toolCount += n
		r.registerMCPResourceTools(name, client)
	}
	return ready, toolCount
}

func (r *Registry) loadMCPConfig() map[string]mcpServerConfig {
	merged := map[string]mcpServerConfig{}
	paths := []string{
		filepath.Join(configHome(), ".zcoder", "mcp.json"),
		filepath.Join(r.root, ".zcoder", "mcp.json"),
	}
	for _, path := range paths {
		b, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var cfg mcpConfigFile
		if json.Unmarshal(b, &cfg) != nil {
			continue
		}
		for name, server := range cfg.MCPServers {
			expandMCPVars(&server, r.root)
			merged[name] = server
		}
	}
	if os.Getenv("STEP_API_KEY") != "" {
		if _, ok := merged["step_search"]; !ok {
			merged["step_search"] = mcpServerConfig{
				URL: "https://api.stepfun.com/step_plan/v1/mcp/web_search/mcp",
				Headers: map[string]string{
					"Authorization": "Bearer ${STEP_API_KEY}",
				},
			}
			server := merged["step_search"]
			expandMCPVars(&server, r.root)
			merged["step_search"] = server
		}
	}
	return merged
}

func (r *Registry) startMCPServer(ctx context.Context, name string, cfg mcpServerConfig) (mcpClient, error) {
	if cfg.Command != "" {
		return startStdioMCP(ctx, r.root, cfg)
	}
	if cfg.URL != "" {
		return &httpMCPClient{url: cfg.URL, headers: cfg.Headers, http: &http.Client{Timeout: 60 * time.Second}}, nil
	}
	return nil, fmt.Errorf("mcp server %s missing command or url", name)
}

func (r *Registry) initializeMCP(ctx context.Context, client mcpClient) error {
	_, err := client.Call(ctx, "initialize", map[string]any{
		"protocolVersion": "2025-03-26",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "zcoder-go", "version": "0.1.0"},
	})
	return err
}

func (r *Registry) registerMCPTools(ctx context.Context, serverName string, client mcpClient) int {
	raw, err := client.Call(ctx, "tools/list", map[string]any{})
	if err != nil {
		return 0
	}
	var out struct {
		Tools []struct {
			Name        string         `json:"name"`
			Description string         `json:"description"`
			InputSchema map[string]any `json:"inputSchema"`
		} `json:"tools"`
	}
	if json.Unmarshal(raw, &out) != nil {
		return 0
	}
	for _, item := range out.Tools {
		localName := "mcp__" + sanitizeName(serverName) + "__" + sanitizeName(item.Name)
		remoteName := item.Name
		desc := item.Description
		schema := sanitizeSchema(item.InputSchema)
		r.register(Tool{
			Name:        localName,
			Description: "MCP " + serverName + "." + remoteName + ": " + desc,
			Parameters:  schema,
			Dangerous:   true,
			Executor: func(ctx context.Context, args map[string]any) (string, error) {
				raw, err := client.Call(ctx, "tools/call", map[string]any{"name": remoteName, "arguments": args})
				if err != nil {
					return "", err
				}
				return flattenMCPContent(raw), nil
			},
		})
	}
	return len(out.Tools)
}

func (r *Registry) registerMCPResourceTools(serverName string, client mcpClient) {
	prefix := "mcp__" + sanitizeName(serverName)
	r.register(Tool{
		Name:        prefix + "__list_resources",
		Description: "List MCP resources for server " + serverName,
		Parameters:  objectParams(),
		Executor: func(ctx context.Context, args map[string]any) (string, error) {
			raw, err := client.Call(ctx, "resources/list", map[string]any{})
			if err != nil {
				return "", err
			}
			return string(raw), nil
		},
	})
	r.register(Tool{
		Name:        prefix + "__read_resource",
		Description: "Read a MCP resource by URI from server " + serverName,
		Parameters:  objectParams(required("uri", "string", "resource URI")),
		Executor: func(ctx context.Context, args map[string]any) (string, error) {
			raw, err := client.Call(ctx, "resources/read", map[string]any{"uri": strArg(args, "uri")})
			if err != nil {
				return "", err
			}
			return flattenMCPContent(raw), nil
		},
	})
}

type stdioMCPClient struct {
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	scanner *bufio.Scanner
	mu      sync.Mutex
	nextID  int64
	closed  atomic.Bool
}

func startStdioMCP(ctx context.Context, root string, cfg mcpServerConfig) (*stdioMCPClient, error) {
	cmd := exec.CommandContext(ctx, cfg.Command, cfg.Args...)
	cmd.Dir = root
	cmd.Env = os.Environ()
	for k, v := range cfg.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, _ := cmd.StderrPipe()
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	if stderr != nil {
		go io.Copy(io.Discard, stderr)
	}
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 1024), 8*1024*1024)
	return &stdioMCPClient{cmd: cmd, stdin: stdin, scanner: sc}, nil
}

func (c *stdioMCPClient) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed.Load() {
		return nil, fmt.Errorf("mcp stdio client closed")
	}
	id := atomic.AddInt64(&c.nextID, 1)
	req := map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}
	b, _ := json.Marshal(req)
	if _, err := c.stdin.Write(append(b, '\n')); err != nil {
		return nil, err
	}
	type response struct {
		ID     int64           `json:"id"`
		Result json.RawMessage `json:"result"`
		Error  any             `json:"error"`
	}
	deadline := time.After(60 * time.Second)
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-deadline:
			return nil, fmt.Errorf("mcp call timeout: %s", method)
		default:
		}
		if !c.scanner.Scan() {
			return nil, fmt.Errorf("mcp server closed stdout")
		}
		var resp response
		if json.Unmarshal(c.scanner.Bytes(), &resp) != nil {
			continue
		}
		if resp.ID != id {
			continue
		}
		if resp.Error != nil {
			errb, _ := json.Marshal(resp.Error)
			return nil, fmt.Errorf("mcp error: %s", errb)
		}
		return resp.Result, nil
	}
}

func (c *stdioMCPClient) Close() error {
	c.closed.Store(true)
	if c.stdin != nil {
		_ = c.stdin.Close()
	}
	if c.cmd != nil && c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
		_, _ = c.cmd.Process.Wait()
	}
	return nil
}

type httpMCPClient struct {
	url     string
	headers map[string]string
	http    *http.Client
	nextID  int64
}

func (c *httpMCPClient) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := atomic.AddInt64(&c.nextID, 1)
	reqBody, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(reqBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range c.headers {
		req.Header.Set(k, v)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("mcp http failed: %s: %s", resp.Status, string(data))
	}
	var out struct {
		Result json.RawMessage `json:"result"`
		Error  any             `json:"error"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	if out.Error != nil {
		errb, _ := json.Marshal(out.Error)
		return nil, fmt.Errorf("mcp error: %s", errb)
	}
	return out.Result, nil
}

func (c *httpMCPClient) Close() error { return nil }

func expandMCPVars(cfg *mcpServerConfig, project string) {
	repl := func(s string) string {
		s = strings.ReplaceAll(s, "${PROJECT_DIR}", project)
		s = strings.ReplaceAll(s, "${HOME}", configHome())
		for _, env := range os.Environ() {
			parts := strings.SplitN(env, "=", 2)
			s = strings.ReplaceAll(s, "${"+parts[0]+"}", parts[1])
		}
		return s
	}
	cfg.Command = repl(cfg.Command)
	cfg.URL = repl(cfg.URL)
	for i, arg := range cfg.Args {
		cfg.Args[i] = repl(arg)
	}
	for k, v := range cfg.Headers {
		cfg.Headers[k] = repl(v)
	}
	for k, v := range cfg.Env {
		cfg.Env[k] = repl(v)
	}
}

func sanitizeName(s string) string {
	s = strings.ReplaceAll(s, "-", "_")
	s = strings.ReplaceAll(s, ".", "_")
	return s
}

func sanitizeSchema(schema map[string]any) map[string]any {
	if len(schema) == 0 {
		return objectParams()
	}
	if _, ok := schema["type"]; !ok {
		schema["type"] = "object"
	}
	return schema
}

func flattenMCPContent(raw json.RawMessage) string {
	var out struct {
		Content []struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			MimeType string `json:"mimeType"`
			Data     string `json:"data"`
		} `json:"content"`
	}
	if json.Unmarshal(raw, &out) != nil || len(out.Content) == 0 {
		return string(raw)
	}
	var b strings.Builder
	for _, c := range out.Content {
		if c.Type == "text" {
			b.WriteString(c.Text)
			b.WriteString("\n")
		} else {
			b.WriteString("[" + c.Type + " content")
			if c.MimeType != "" {
				b.WriteString(" " + c.MimeType)
			}
			if c.Data != "" {
				b.WriteString(" base64=" + fmt.Sprint(len(c.Data)) + " bytes")
			}
			b.WriteString("]\n")
		}
	}
	return strings.TrimSpace(b.String())
}

func configHome() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return home
}
