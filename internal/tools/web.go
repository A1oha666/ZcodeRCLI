package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

type searchRow struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Content string `json:"content"`
}

func (r *Registry) webSearch(ctx context.Context, query string, n int) (string, error) {
	if query == "" {
		return "", fmt.Errorf("query is required")
	}
	if n <= 0 || n > 10 {
		n = 5
	}
	if r.cfg.Web.SearxngURL != "" {
		return searxngSearch(ctx, r.cfg.Web.SearxngURL, query, n)
	}
	if r.cfg.Web.SerpAPIKey != "" {
		return serpAPISearch(ctx, r.cfg.Web.SerpAPIKey, query, n)
	}
	return duckDuckGoSearch(ctx, query, n)
}

func searxngSearch(ctx context.Context, base, query string, n int) (string, error) {
	u, _ := url.Parse(strings.TrimRight(base, "/") + "/search")
	q := u.Query()
	q.Set("q", query)
	q.Set("format", "json")
	u.RawQuery = q.Encode()
	data, err := httpGet(ctx, u.String(), 2<<20)
	if err != nil {
		return "", err
	}
	var out struct {
		Results []searchRow `json:"results"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return "", err
	}
	return formatSearch(out.Results, n), nil
}

func serpAPISearch(ctx context.Context, key, query string, n int) (string, error) {
	u, _ := url.Parse("https://serpapi.com/search.json")
	q := u.Query()
	q.Set("q", query)
	q.Set("api_key", key)
	q.Set("num", fmt.Sprint(n))
	u.RawQuery = q.Encode()
	data, err := httpGet(ctx, u.String(), 2<<20)
	if err != nil {
		return "", err
	}
	var out struct {
		Organic []struct {
			Title   string `json:"title"`
			Link    string `json:"link"`
			Snippet string `json:"snippet"`
		} `json:"organic_results"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return "", err
	}
	rows := make([]searchRow, 0, len(out.Organic))
	for _, item := range out.Organic {
		rows = append(rows, searchRow{Title: item.Title, URL: item.Link, Content: item.Snippet})
	}
	return formatSearch(rows, n), nil
}

func duckDuckGoSearch(ctx context.Context, query string, n int) (string, error) {
	u, _ := url.Parse("https://duckduckgo.com/html/")
	q := u.Query()
	q.Set("q", query)
	u.RawQuery = q.Encode()
	data, err := httpGet(ctx, u.String(), 2<<20)
	if err != nil {
		return "", fmt.Errorf("web_search fallback failed; configure SERPAPI_API_KEY or SEARXNG_BASE_URL for a more reliable provider: %w", err)
	}
	return formatSearch(parseDuckDuckGoHTML(string(data)), n), nil
}

var duckTitleRE = regexp.MustCompile(`(?is)<a[^>]+class="result__a"[^>]+href="([^"]+)"[^>]*>(.*?)</a>`)
var duckSnippetRE = regexp.MustCompile(`(?is)<(?:a|div)[^>]+class="result__snippet"[^>]*>(.*?)</(?:a|div)>`)

func parseDuckDuckGoHTML(page string) []searchRow {
	indexes := duckTitleRE.FindAllStringSubmatchIndex(page, -1)
	rows := make([]searchRow, 0, len(indexes))
	for i, index := range indexes {
		if len(index) < 6 {
			continue
		}
		segmentEnd := len(page)
		if i+1 < len(indexes) {
			segmentEnd = indexes[i+1][0]
		}
		segment := page[index[1]:segmentEnd]
		row := searchRow{
			Title:   cleanSearchHTML(page[index[4]:index[5]]),
			URL:     decodeDuckDuckGoURL(html.UnescapeString(page[index[2]:index[3]])),
			Content: "",
		}
		if snippet := duckSnippetRE.FindStringSubmatch(segment); len(snippet) > 1 {
			row.Content = cleanSearchHTML(snippet[1])
		}
		if row.Title != "" && row.URL != "" {
			rows = append(rows, row)
		}
	}
	return rows
}

func decodeDuckDuckGoURL(raw string) string {
	if strings.HasPrefix(raw, "//") {
		raw = "https:" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	if strings.Contains(u.Hostname(), "duckduckgo.com") {
		if target := u.Query().Get("uddg"); target != "" {
			if decoded, err := url.QueryUnescape(target); err == nil {
				return decoded
			}
			return target
		}
	}
	return raw
}

func cleanSearchHTML(fragment string) string {
	text := tagRE.ReplaceAllString(fragment, " ")
	text = html.UnescapeString(text)
	return strings.Join(strings.Fields(text), " ")
}

func formatSearch(items []searchRow, n int) string {
	var b strings.Builder
	limit := n
	if len(items) < limit {
		limit = len(items)
	}
	for i := 0; i < limit; i++ {
		item := items[i]
		b.WriteString(fmt.Sprintf("%d. %s\n%s\n%s\n\n", i+1, item.Title, item.URL, item.Content))
	}
	if b.Len() == 0 {
		return "no results"
	}
	return strings.TrimSpace(b.String())
}

func webFetch(ctx context.Context, rawURL string, maxChars int) (string, error) {
	if maxChars <= 0 || maxChars > 60000 {
		maxChars = 8000
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("blocked URL scheme: %s", u.Scheme)
	}
	host := u.Hostname()
	if ip := net.ParseIP(host); ip != nil && !ip.IsGlobalUnicast() {
		return "", fmt.Errorf("blocked non-public IP: %s", host)
	}
	data, err := httpGet(ctx, rawURL, 5<<20)
	if err != nil {
		return "", err
	}
	text := extractHTMLText(string(data))
	if len(text) > maxChars {
		text = text[:maxChars] + "\n... truncated"
	}
	return text, nil
}

func httpGet(ctx context.Context, rawURL string, maxBytes int64) ([]byte, error) {
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Zcoder-Go/0.1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("fetch failed: %s", resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, maxBytes))
}

var scriptStyleRE = regexp.MustCompile(`(?is)<script[^>]*>.*?</script>|<style[^>]*>.*?</style>|<noscript[^>]*>.*?</noscript>`)
var tagRE = regexp.MustCompile(`(?s)<[^>]+>`)
var spaceRE = regexp.MustCompile(`[ \t\r\f]+`)
var blankRE = regexp.MustCompile(`\n{3,}`)

func extractHTMLText(html string) string {
	html = scriptStyleRE.ReplaceAllString(html, " ")
	html = strings.ReplaceAll(html, "</p>", "</p>\n")
	html = strings.ReplaceAll(html, "<br>", "\n")
	html = strings.ReplaceAll(html, "<br/>", "\n")
	text := tagRE.ReplaceAllString(html, " ")
	replacer := strings.NewReplacer("&nbsp;", " ", "&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", `"`, "&#39;", "'")
	text = replacer.Replace(text)
	text = spaceRE.ReplaceAllString(text, " ")
	text = blankRE.ReplaceAllString(text, "\n\n")
	return strings.TrimSpace(text)
}
