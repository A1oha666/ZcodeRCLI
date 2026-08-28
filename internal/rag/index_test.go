package rag

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIndexBuildAndSearchGoFunction(t *testing.T) {
	root := t.TempDir()
	src := `package demo

import "fmt"

func HelloZcoder() {
	fmt.Println("hello")
}
`
	if err := os.WriteFile(filepath.Join(root, "demo.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	idx := NewIndex(filepath.Join(root, "index.json"))
	if err := idx.Build(root); err != nil {
		t.Fatal(err)
	}
	results := idx.Search("HelloZcoder", 3)
	if len(results) == 0 {
		t.Fatal("expected search results")
	}
	if results[0].Chunk.Symbol != "HelloZcoder" {
		t.Fatalf("expected function chunk first, got %#v", results[0].Chunk)
	}
	if len(idx.Relations) == 0 {
		t.Fatal("expected relations")
	}
}
