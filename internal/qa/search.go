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
// top results as {title, snippet, url}. Results are cached on disk for
// 6 hours so repeated questions don't re-search.
type searchEntry struct {
	Results   json.RawMessage `json:"results"`
	FetchedAt time.Time       `json:"fetched_at"`
}

const searchTTL = 6 * time.Hour

const browserUA = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0 Safari/537.36"

var (
	ddgResultRe  = regexp.MustCompile(`(?s)<a[^>]+class="result__a"[^>]*href="([^"]*)"[^>]*>(.*?)</a>`)
	ddgSnippetRe = regexp.MustCompile(`(?s)<a[^>]+class="result__snippet"[^>]*>(.*?)</a>`)
	tagRe        = regexp.MustCompile(`<[^>]+>`)
	dropBlockRe  = regexp.MustCompile(`(?is)<script[^>]*>.*?</script>|<style[^>]*>.*?</style>|<noscript[^>]*>.*?</noscript>`)
	spaceRe      = regexp.MustCompile(`\s+`)
)

func (h *Handler) webSearch(ctx context.Context, query string) []searchResult {
	key := "search-" + sanitizeKey(query)
	var cached searchEntry
	if err := h.profiles.store.LoadJSON("wcdb", key, &cached); err == nil &&
		time.Since(cached.FetchedAt) < searchTTL && len(cached.Results) > 0 {
		var results []searchResult
		if json.Unmarshal(cached.Results, &results) == nil && len(results) > 0 {
			return results
		}
	}

	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodPost, "https://html.duckduckgo.com/html/",
		strings.NewReader(url.Values{"q": {query}}.Encode()))
	if err != nil {
		return nil
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", browserUA)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.logger.Error("web search failed", "query", query, "error", err)
		return nil
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil
	}
	results := parseDDG(string(body), 5)
	if len(results) == 0 {
		h.logger.Warn("web search returned nothing", "query", query, "status", resp.StatusCode)
		return nil
	}
	raw, err := json.Marshal(results)
	if err != nil {
		return nil
	}
	if err := h.profiles.store.SaveJSON("wcdb", key, searchEntry{Results: raw, FetchedAt: time.Now()}); err != nil {
		h.logger.Error("persist search cache failed", "error", err)
	}
	h.logger.Info("web search cached", "query", query, "results", len(results))
	return results
}

type searchResult struct {
	Title   string `json:"title"`
	Snippet string `json:"snippet"`
	URL     string `json:"url,omitempty"`
}

func parseDDG(body string, limit int) []searchResult {
	titles := ddgResultRe.FindAllStringSubmatch(body, limit)
	snippets := ddgSnippetRe.FindAllStringSubmatch(body, limit)
	var out []searchResult
	for i, t := range titles {
		r := searchResult{Title: cleanHTML(t[2]), URL: ddgTargetURL(t[1])}
		if i < len(snippets) {
			r.Snippet = cleanHTML(snippets[i][1])
		}
		if r.Title != "" {
			out = append(out, r)
		}
	}
	return out
}

// ddgTargetURL unwraps DuckDuckGo's redirect href (//duckduckgo.com/l/?uddg=
// <encoded>) into the real destination URL.
func ddgTargetURL(href string) string {
	href = html.UnescapeString(href)
	u, err := url.Parse(href)
	if err != nil {
		return ""
	}
	if target := u.Query().Get("uddg"); target != "" {
		return target
	}
	if u.Scheme == "http" || u.Scheme == "https" {
		return href
	}
	return ""
}

// trustedStatDomains are sources whose page text is worth fetching when
// verifying a factual claim; ordering in search results decides which one.
var trustedStatDomains = []string{
	"fbref.com", "espn.com", "bbc.com", "bbc.co.uk", "skysports.com",
	"premierleague.com", "transfermarkt.", "statmuse.com", "sofascore.com",
	"flashscore.com", "worldfootball.net", "goal.com", "theguardian.com",
	"wikipedia.org", "dongqiudi.com", "hupu.com", "zhibo8.cc",
}

func trustedSource(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return false
	}
	host := strings.ToLower(u.Host)
	for _, d := range trustedStatDomains {
		if strings.Contains(host, d) {
			return true
		}
	}
	return false
}

// fetchPageText downloads a page and reduces it to plain text (capped) so
// the fact verifier can read the actual numbers instead of search snippets.
func fetchPageText(ctx context.Context, rawURL string) string {
	cctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return ""
	}
	req.Header.Set("User-Agent", browserUA)
	req.Header.Set("Accept-Language", "en,zh-CN;q=0.8")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return ""
	}
	text := dropBlockRe.ReplaceAllString(string(body), " ")
	text = tagRe.ReplaceAllString(text, " ")
	text = html.UnescapeString(text)
	text = strings.TrimSpace(spaceRe.ReplaceAllString(text, " "))
	if runes := []rune(text); len(runes) > 3500 {
		text = string(runes[:3500])
	}
	return text
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
