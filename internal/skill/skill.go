package skill

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

type Skill struct {
	Name        string
	Description string
	Version     string
	Author      string
	Tags        []string
	Source      string
	Path        string
	Raw         string
	Body        string
	Enabled     bool
}

type Registry struct {
	projectRoot string
	statePath   string
	mu          sync.RWMutex
	skills      map[string]Skill
	disabled    map[string]bool
	buffer      []LoadedBody
}

type LoadedBody struct {
	Name string
	Body string
}

func NewRegistry(projectRoot string) *Registry {
	home, _ := os.UserHomeDir()
	return &Registry{
		projectRoot: projectRoot,
		statePath:   filepath.Join(home, ".zcoder", "skills.json"),
		skills:      map[string]Skill{},
		disabled:    map[string]bool{},
	}
}

func (r *Registry) Reload() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.disabled = loadState(r.statePath)
	next := map[string]Skill{}
	for _, sk := range builtinSkills() {
		sk.Enabled = !r.disabled[sk.Name]
		next[sk.Name] = sk
	}
	for _, root := range []struct {
		path   string
		source string
	}{
		{filepath.Join(homeDir(), ".zcoder", "skills"), "user"},
		{filepath.Join(r.projectRoot, ".zcoder", "skills"), "project"},
	} {
		found, err := scanSkills(root.path, root.source)
		if err != nil {
			continue
		}
		for _, sk := range found {
			sk.Enabled = !r.disabled[sk.Name]
			next[sk.Name] = sk
		}
	}
	r.skills = next
	return nil
}

func (r *Registry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.skills)
}

func (r *Registry) EnabledCount() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	n := 0
	for _, sk := range r.skills {
		if sk.Enabled {
			n++
		}
	}
	return n
}

func (r *Registry) All() []Skill {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Skill, 0, len(r.skills))
	for _, sk := range r.skills {
		out = append(out, sk)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (r *Registry) IndexForPrompt(limit int) string {
	var b strings.Builder
	n := 0
	for _, sk := range r.All() {
		if !sk.Enabled {
			continue
		}
		line := fmt.Sprintf("- %s: %s\n", sk.Name, compact(sk.Description, 500))
		if b.Len()+len(line) > limit {
			break
		}
		b.WriteString(line)
		n++
		if n >= 20 {
			break
		}
	}
	if b.Len() == 0 {
		return ""
	}
	return "## Available Skills\n" + b.String()
}

func (r *Registry) Load(name string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	sk, ok := r.skills[name]
	if !ok || !sk.Enabled {
		return "", fmt.Errorf("skill %q not found or disabled", name)
	}
	body := sk.Body
	if len(body) > 5000 {
		body = body[:5000] + "\n\n(skill body truncated, full content via /skill show " + name + ")"
	}
	r.buffer = append(r.buffer, LoadedBody{Name: name, Body: body})
	return fmt.Sprintf("Loaded skill %q (%d bytes); it will be injected into the next user turn.", name, len(body)), nil
}

func (r *Registry) ConsumeLoadedBodies() []LoadedBody {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := append([]LoadedBody(nil), r.buffer...)
	r.buffer = nil
	return out
}

func scanSkills(root, source string) ([]Skill, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	var out []Skill
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		path := filepath.Join(root, entry.Name(), "SKILL.md")
		b, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		sk, err := parseSkill(string(b), source, path)
		if err != nil {
			continue
		}
		out = append(out, sk)
	}
	return out, nil
}

func parseSkill(raw, source, path string) (Skill, error) {
	meta, body := parseFrontmatter(raw)
	name := strings.TrimSpace(meta["name"])
	if name == "" {
		name = filepath.Base(filepath.Dir(path))
	}
	description := strings.TrimSpace(meta["description"])
	if description == "" {
		return Skill{}, fmt.Errorf("skill %s missing description", name)
	}
	return Skill{
		Name:        name,
		Description: description,
		Version:     meta["version"],
		Author:      meta["author"],
		Tags:        parseTags(meta["tags"]),
		Source:      source,
		Path:        path,
		Raw:         raw,
		Body:        body,
	}, nil
}

func parseFrontmatter(raw string) (map[string]string, string) {
	meta := map[string]string{}
	if !strings.HasPrefix(raw, "---\n") {
		return meta, raw
	}
	rest := raw[len("---\n"):]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return meta, raw
	}
	fm := rest[:end]
	body := strings.TrimLeft(rest[end+len("\n---"):], "\r\n")
	lines := strings.Split(fm, "\n")
	for i := 0; i < len(lines); i++ {
		line := strings.TrimRight(lines[i], "\r")
		if strings.TrimSpace(line) == "" || !strings.Contains(line, ":") {
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])
		if value == "|" {
			var block []string
			for i+1 < len(lines) && (strings.HasPrefix(lines[i+1], " ") || strings.TrimSpace(lines[i+1]) == "") {
				i++
				block = append(block, strings.TrimSpace(lines[i]))
			}
			value = strings.Join(block, " ")
		}
		meta[key] = strings.Trim(value, `"'`)
	}
	return meta, body
}

func parseTags(raw string) []string {
	raw = strings.Trim(raw, "[]")
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if s := strings.Trim(strings.TrimSpace(part), `"'`); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func loadState(path string) map[string]bool {
	out := map[string]bool{}
	b, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	var state struct {
		Disabled []string `json:"disabled"`
	}
	if json.Unmarshal(b, &state) != nil {
		return out
	}
	for _, name := range state.Disabled {
		out[name] = true
	}
	return out
}

func compact(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func homeDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return home
}

func builtinSkills() []Skill {
	raw := builtinWebAccess
	sk, _ := parseSkill(raw, "builtin", "builtin:web-access/SKILL.md")
	return []Skill{sk}
}
