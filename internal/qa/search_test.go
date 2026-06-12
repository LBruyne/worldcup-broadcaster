package qa

import (
	"strings"
	"testing"
)

func TestExcerptAround(t *testing.T) {
	if got := excerptAround("short text", "short", 100); got != "short text" {
		t.Errorf("short = %q", got)
	}
	// the stat sits deep in the page after nav boilerplate
	text := strings.Repeat("nav menu login cookie ", 400) + "Bruno Fernandes 2025/26: 9 goals, 21 assists" + strings.Repeat(" footer", 200)
	got := excerptAround(text, "Bruno Fernandes 2025-26 stats", 1000)
	if !strings.Contains(got, "21 assists") {
		t.Errorf("hint windowing missed the stats: %.120q", got)
	}
	if n := len([]rune(got)); n > 1000 {
		t.Errorf("excerpt too long: %d", n)
	}
	// no hint hit: falls back to head
	head := excerptAround(text, "zzzqqq", 50)
	if !strings.HasPrefix(head, "nav menu") {
		t.Errorf("fallback = %.60q", head)
	}
}

func TestEnglishQueries(t *testing.T) {
	ev := map[string]any{
		"搜索（Bruno Fernandes 2025-26 season stats）": nil,
		"搜索（B费 2025-26赛季 表现 数据）":                   nil,
		"页面正文（https://x）":                          nil,
	}
	qs := englishQueries(ev)
	if len(qs) != 1 || qs[0] != "Bruno Fernandes 2025-26 season stats" {
		t.Errorf("englishQueries = %v", qs)
	}
}
