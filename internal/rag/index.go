package rag

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

type Index struct {
	Path      string     `json:"-"`
	Root      string     `json:"root"`
	Chunks    []Chunk    `json:"chunks"`
	Relations []Relation `json:"relations"`
}

type Chunk struct {
	Path      string         `json:"path"`
	StartLine int            `json:"start_line"`
	EndLine   int            `json:"end_line"`
	Kind      string         `json:"kind"`
	Symbol    string         `json:"symbol"`
	Text      string         `json:"text"`
	Terms     map[string]int `json:"terms"`
}

type Relation struct {
	From string `json:"from"`
	To   string `json:"to"`
	Kind string `json:"kind"`
}

type Result struct {
	Chunk Chunk
	Score float64
}

func NewIndex(path string) *Index {
	return &Index{Path: path}
}

func (i *Index) Build(root string) error {
	abs, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	i.Root = abs
	i.Chunks = nil
	i.Relations = nil
	return filepath.WalkDir(abs, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", ".zcoder", "node_modules", "target", "dist", "build", "coverage", "vendor":
				return filepath.SkipDir
			}
			return nil
		}
		if !isCodeFile(path) {
			return nil
		}
		return i.addFile(path)
	})
}

func (i *Index) addFile(path string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	rel, _ := filepath.Rel(i.Root, path)
	text := string(b)
	lines := strings.Count(text, "\n") + 1
	i.Chunks = append(i.Chunks, Chunk{
		Path:      rel,
		StartLine: 1,
		EndLine:   lines,
		Kind:      "file",
		Symbol:    rel,
		Text:      trimText(text, 8000),
		Terms:     terms(text),
	})
	if filepath.Ext(path) == ".go" {
		i.addGoSymbols(path, rel, text)
	}
	return nil
}

func (i *Index) addGoSymbols(path, rel, text string) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, text, parser.ParseComments)
	if err != nil {
		return
	}
	importer := rel
	for _, spec := range file.Imports {
		to := strings.Trim(spec.Path.Value, `"`)
		i.Relations = append(i.Relations, Relation{From: importer, To: to, Kind: "imports"})
	}
	ast.Inspect(file, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if !ok {
			return true
		}
		start := fset.Position(fn.Pos()).Line
		end := fset.Position(fn.End()).Line
		snippet := lines(text, start, end)
		sym := fn.Name.Name
		if fn.Recv != nil && len(fn.Recv.List) > 0 {
			sym = exprString(fn.Recv.List[0].Type) + "." + sym
		}
		i.Chunks = append(i.Chunks, Chunk{
			Path:      rel,
			StartLine: start,
			EndLine:   end,
			Kind:      "function",
			Symbol:    sym,
			Text:      trimText(snippet, 8000),
			Terms:     terms(sym + "\n" + snippet),
		})
		i.Relations = append(i.Relations, Relation{From: rel, To: sym, Kind: "contains"})
		return false
	})
}

func (i *Index) Save() error {
	if err := os.MkdirAll(filepath.Dir(i.Path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(i, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(i.Path, b, 0o600)
}

func (i *Index) Load() error {
	b, err := os.ReadFile(i.Path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, i)
}

func (i *Index) Search(query string, topK int) []Result {
	if topK <= 0 {
		topK = 8
	}
	q := terms(query)
	var out []Result
	for _, chunk := range i.Chunks {
		score := cosine(q, chunk.Terms)
		if strings.Contains(strings.ToLower(chunk.Path), strings.ToLower(query)) {
			score += 0.4
		}
		if strings.Contains(strings.ToLower(chunk.Symbol), strings.ToLower(query)) {
			score += 0.8
		}
		if score > 0 {
			out = append(out, Result{Chunk: chunk, Score: score})
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Score > out[b].Score })
	if len(out) > topK {
		out = out[:topK]
	}
	return out
}

func (c Chunk) Preview() string {
	return trimText(strings.TrimSpace(c.Text), 500)
}

var tokenRE = regexp.MustCompile(`[A-Za-z0-9_\p{Han}]+`)

func terms(s string) map[string]int {
	out := map[string]int{}
	for _, t := range tokenRE.FindAllString(strings.ToLower(s), -1) {
		if len([]rune(t)) < 2 {
			continue
		}
		out[t]++
	}
	return out
}

func cosine(a, b map[string]int) float64 {
	var dot, aa, bb float64
	for k, av := range a {
		aa += float64(av * av)
		if bv, ok := b[k]; ok {
			dot += float64(av * bv)
		}
	}
	for _, bv := range b {
		bb += float64(bv * bv)
	}
	if aa == 0 || bb == 0 {
		return 0
	}
	return dot / (sqrt(aa) * sqrt(bb))
}

func sqrt(v float64) float64 {
	z := v
	for n := 0; n < 12; n++ {
		z -= (z*z - v) / (2 * z)
	}
	return z
}

func isCodeFile(path string) bool {
	switch filepath.Ext(path) {
	case ".go", ".java", ".ts", ".tsx", ".js", ".jsx", ".py", ".md", ".yaml", ".yml", ".json":
		return true
	default:
		return false
	}
}

func trimText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "\n..."
}

func lines(text string, start, end int) string {
	all := strings.Split(text, "\n")
	if start < 1 {
		start = 1
	}
	if end > len(all) {
		end = len(all)
	}
	if start > end {
		return ""
	}
	return strings.Join(all[start-1:end], "\n")
}

func exprString(expr ast.Expr) string {
	switch x := expr.(type) {
	case *ast.StarExpr:
		return "*" + exprString(x.X)
	case *ast.Ident:
		return x.Name
	case *ast.SelectorExpr:
		return exprString(x.X) + "." + x.Sel.Name
	default:
		return "receiver"
	}
}
