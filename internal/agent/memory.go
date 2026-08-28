package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type MemoryStore struct {
	path    string
	project string
	mu      sync.Mutex
	entries []MemoryEntry
}

type MemoryEntry struct {
	ID        string    `json:"id"`
	Scope     string    `json:"scope"`
	Project   string    `json:"project,omitempty"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"created_at"`
}

func NewMemoryStore(path, project string) *MemoryStore {
	m := &MemoryStore{path: path, project: project}
	_ = m.load()
	return m
}

func (m *MemoryStore) Save(content, scope string) error {
	content = strings.TrimSpace(content)
	if content == "" {
		return nil
	}
	if scope == "" {
		scope = "project"
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries = append(m.entries, MemoryEntry{
		ID:        "fact-" + time.Now().Format("20060102150405.000000000"),
		Scope:     scope,
		Project:   m.projectForScope(scope),
		Content:   content,
		CreatedAt: time.Now(),
	})
	return m.persist()
}

func (m *MemoryStore) Relevant(query string, limit int) []MemoryEntry {
	m.mu.Lock()
	defer m.mu.Unlock()
	if limit <= 0 {
		limit = 8
	}
	needle := strings.ToLower(query)
	var out []MemoryEntry
	for _, e := range m.entries {
		if e.Scope != "global" && e.Project != m.project {
			continue
		}
		if needle == "" || strings.Contains(strings.ToLower(e.Content), needle) || overlap(needle, strings.ToLower(e.Content)) {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

func (m *MemoryStore) load() error {
	b, err := os.ReadFile(m.path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, &m.entries)
}

func (m *MemoryStore) persist() error {
	if err := os.MkdirAll(filepath.Dir(m.path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(m.entries, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(m.path, b, 0o600)
}

func (m *MemoryStore) projectForScope(scope string) string {
	if scope == "global" {
		return ""
	}
	return m.project
}

func overlap(query, content string) bool {
	qs := strings.Fields(query)
	if len(qs) == 0 {
		return false
	}
	hits := 0
	for _, q := range qs {
		if len([]rune(q)) >= 2 && strings.Contains(content, q) {
			hits++
		}
	}
	return hits >= 2
}
