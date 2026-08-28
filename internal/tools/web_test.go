package tools

import (
	"strings"
	"testing"
)

func TestExtractHTMLTextDropsScriptAndTags(t *testing.T) {
	got := extractHTMLText(`<html><script>alert(1)</script><style>x{}</style><body><h1>标题</h1><p>Hello&nbsp;World</p></body></html>`)
	if strings.Contains(got, "alert") || strings.Contains(got, "<h1>") {
		t.Fatalf("html cleanup leaked tags/scripts: %q", got)
	}
	if !strings.Contains(got, "标题") || !strings.Contains(got, "Hello World") {
		t.Fatalf("html cleanup lost body text: %q", got)
	}
}

func TestParseDuckDuckGoHTML(t *testing.T) {
	page := `<html><body>
		<a class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com%2Fprofile">沉默王二是谁</a>
		<a class="result__snippet">一个技术作者的介绍</a>
	</body></html>`
	rows := parseDuckDuckGoHTML(page)
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	if rows[0].Title != "沉默王二是谁" {
		t.Fatalf("title = %q", rows[0].Title)
	}
	if rows[0].URL != "https://example.com/profile" {
		t.Fatalf("url = %q", rows[0].URL)
	}
	if rows[0].Content != "一个技术作者的介绍" {
		t.Fatalf("content = %q", rows[0].Content)
	}
}
