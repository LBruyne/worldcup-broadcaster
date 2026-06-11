package qa

import (
	"context"
	"encoding/json"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// webSearch queries DuckDuckGo's HTML endpoint (no API key) and returns the
// top results as a JSON array of {title, snippet}. Results are cached on
// disk for 6 hours so repeated questions don't re-search.
type searchEntry struct {
	Results   json.RawMessage `json:"results"`
	FetchedAt time.Time       `json:"fetched_at"`
}

const searchTTL = 6 * time.Hour

var (
	ddgResultRe  = regexp.MustCompile(`(?s)<a[^>]+class="result__a"[^>]*>(.*?)</a>`)
	ddgSnippetRe = regexp.MustCompile(`(?s)<a[^>]+class="result__snippet"[^>]*>(.*?)</a>`)
	tagRe        = regexp.MustCompile(`<[^>]+>`)
)

func (h *Handler) webSearch(ctx context.Context, query string) string {
	key := "search-" + sanitizeKey(query)
	var cached searchEntry
	if err := h.profiles.store.LoadJSON("wcdb", key, &cached); err == nil &&
		time.Since(cached.FetchedAt) < searchTTL && len(cached.Results) > 0 {
		return string(cached.Results)
	}

	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodPost, "https://html.duckduckgo.com/html/",
		strings.NewReader(url.Values{"q": {query}}.Encode()))
	if err != nil {
		return ""
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0 Safari/537.36")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.logger.Error("web search failed", "query", query, "error", err)
		return ""
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return ""
	}
	results := parseDDG(string(body), 5)
	if len(results) == 0 {
		h.logger.Warn("web search returned nothing", "query", query, "status", resp.StatusCode)
		return ""
	}
	raw, err := json.Marshal(results)
	if err != nil {
		return ""
	}
	if err := h.profiles.store.SaveJSON("wcdb", key, searchEntry{Results: raw, FetchedAt: time.Now()}); err != nil {
		h.logger.Error("persist search cache failed", "error", err)
	}
	h.logger.Info("web search cached", "query", query, "results", len(results))
	return string(raw)
}

type searchResult struct {
	Title   string `json:"title"`
	Snippet string `json:"snippet"`
}

func parseDDG(body string, limit int) []searchResult {
	titles := ddgResultRe.FindAllStringSubmatch(body, limit)
	snippets := ddgSnippetRe.FindAllStringSubmatch(body, limit)
	var out []searchResult
	for i, t := range titles {
		r := searchResult{Title: cleanHTML(t[1])}
		if i < len(snippets) {
			r.Snippet = cleanHTML(snippets[i][1])
		}
		if r.Title != "" {
			out = append(out, r)
		}
	}
	return out
}

func cleanHTML(s string) string {
	return strings.TrimSpace(html.UnescapeString(tagRe.ReplaceAllString(s, "")))
}

// sanitizeKey turns a query into a safe filename fragment.
func sanitizeKey(q string) string {
	q = strings.ToLower(strings.TrimSpace(q))
	var b strings.Builder
	for _, r := range q {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r >= 0x4e00 && r <= 0x9fff:
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	out := b.String()
	if len(out) > 60 {
		out = out[:60]
	}
	return out
}
