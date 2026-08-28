package snapshot

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type Service struct {
	Root string
	Dir  string
}

type Item struct {
	ID      string    `json:"id"`
	Phase   string    `json:"phase"`
	Created time.Time `json:"created"`
	Path    string    `json:"path"`
}

func NewService(root, base string) *Service {
	abs, _ := filepath.Abs(root)
	sum := sha1.Sum([]byte(abs))
	id := hex.EncodeToString(sum[:])[:16]
	return &Service{Root: abs, Dir: filepath.Join(base, id)}
}

func (s *Service) Create(phase string) (Item, error) {
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return Item{}, err
	}
	item := Item{
		ID:      time.Now().Format("20060102150405.000000000"),
		Phase:   phase,
		Created: time.Now(),
	}
	item.Path = filepath.Join(s.Dir, item.ID)
	if err := copyTree(s.Root, item.Path); err != nil {
		return Item{}, err
	}
	b, _ := json.MarshalIndent(item, "", "  ")
	return item, os.WriteFile(filepath.Join(item.Path, "snapshot.json"), b, 0o600)
}

func (s *Service) List() ([]Item, error) {
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		return nil, err
	}
	var out []Item
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(s.Dir, e.Name(), "snapshot.json"))
		if err != nil {
			continue
		}
		var item Item
		if json.Unmarshal(b, &item) == nil {
			out = append(out, item)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created.After(out[j].Created) })
	return out, nil
}

func (s *Service) Restore(id string) error {
	items, err := s.List()
	if err != nil {
		return err
	}
	var target Item
	for _, item := range items {
		if item.ID == id {
			target = item
			break
		}
	}
	if target.ID == "" {
		return fmt.Errorf("snapshot %s not found", id)
	}
	if _, err := s.Create("pre-restore"); err != nil {
		return err
	}
	return copyTree(target.Path, s.Root)
}

func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(src, path)
		if rel == "snapshot.json" {
			return nil
		}
		if rel == "." || shouldSkip(rel) {
			if d.IsDir() && rel != "." {
				return filepath.SkipDir
			}
			return nil
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		if info.Size() > 20*1024*1024 {
			return nil
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return copyFile(path, target, info.Mode())
	})
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

func shouldSkip(rel string) bool {
	parts := strings.Split(filepath.ToSlash(rel), "/")
	for _, p := range parts {
		switch p {
		case ".git", ".zcoder", "node_modules", "target", "dist", "build", "coverage", "vendor":
			return true
		}
	}
	return false
}
