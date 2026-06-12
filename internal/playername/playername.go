// Package playername translates footballer names to their conventional
// Chinese media renderings via the LLM, with a persistent cache so each
// name is translated at most once per tournament.
package playername

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"time"

	"worldcup-broadcaster/internal/store"
)

type LLM interface {
	Generate(ctx context.Context, system, user string) (string, error)
}

const translatePrompt = `把这些足球运动员的英文名翻译成中文（使用国内主流体育媒体的通用译名，例如 Son Heung-Min→孙兴慜、Kylian Mbappé→姆巴佩、Harry Kane→凯恩；韩国/日本球员用其惯用汉字名）。只输出一行JSON对象，键为输入的英文原名、值为中文译名，不要任何其他文字。没把握的名字按发音规则音译。`

type Translator struct {
	mu     sync.Mutex
	names  map[string]string
	st     *store.Store
	llm    LLM
	logger *slog.Logger
}

func New(st *store.Store, llm LLM, logger *slog.Logger) *Translator {
	t := &Translator{names: map[string]string{}, st: st, llm: llm, logger: logger}
	_ = st.LoadJSON("playernames", "map", &t.names)
	return t
}

// Name returns the cached Chinese rendering, or the input unchanged.
func (t *Translator) Name(en string) string {
	if t == nil || en == "" {
		return en
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if zh, ok := t.names[en]; ok && zh != "" {
		return zh
	}
	return en
}

// EnsureBatch translates any not-yet-cached names in one LLM call and
// persists the merged map. Failures degrade to English silently.
func (t *Translator) EnsureBatch(ctx context.Context, names []string) {
	if t == nil || t.llm == nil {
		return
	}
	t.mu.Lock()
	var missing []string
	seen := map[string]bool{}
	for _, n := range names {
		n = strings.TrimSpace(n)
		if n == "" || seen[n] {
			continue
		}
		seen[n] = true
		if _, ok := t.names[n]; !ok {
			missing = append(missing, n)
		}
	}
	t.mu.Unlock()
	if len(missing) == 0 {
		return
	}
	payload, err := json.Marshal(missing)
	if err != nil {
		return
	}
	cctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	out, err := t.llm.Generate(cctx, translatePrompt, string(payload))
	if err != nil {
		t.logger.Error("player name translation failed", "error", err, "names", len(missing))
		return
	}
	if i := strings.Index(out, "{"); i >= 0 {
		if j := strings.LastIndex(out, "}"); j > i {
			out = out[i : j+1]
		}
	}
	got := map[string]string{}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.logger.Error("player name translation parse failed", "error", err, "raw", out)
		return
	}
	t.mu.Lock()
	for en, zh := range got {
		if zh = strings.TrimSpace(zh); zh != "" {
			t.names[en] = zh
		}
	}
	snapshot := make(map[string]string, len(t.names))
	for k, v := range t.names {
		snapshot[k] = v
	}
	t.mu.Unlock()
	if err := t.st.SaveJSON("playernames", "map", snapshot); err != nil {
		t.logger.Error("persist player names failed", "error", err)
	}
	t.logger.Info("player names translated", "new", len(got), "total", len(snapshot))
}
