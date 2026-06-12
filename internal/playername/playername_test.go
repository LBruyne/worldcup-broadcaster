package playername

import (
	"context"
	"log/slog"
	"testing"

	"worldcup-broadcaster/internal/store"
)

type fakeLLM struct{ out string }

func (f *fakeLLM) Generate(_ context.Context, _, _ string) (string, error) { return f.out, nil }

func TestTranslateAndCache(t *testing.T) {
	st := store.New(t.TempDir())
	tr := New(st, &fakeLLM{out: `{"Son Heung-Min":"孙兴慜","Ladislav Krejcí":"克雷伊奇"}`}, slog.Default())
	tr.EnsureBatch(context.Background(), []string{"Son Heung-Min", "Ladislav Krejcí"})
	if got := tr.Name("Son Heung-Min"); got != "孙兴慜" {
		t.Errorf("Name = %q", got)
	}
	if got := tr.Name("Unknown Player"); got != "Unknown Player" {
		t.Errorf("fallback = %q", got)
	}
	// persisted: a fresh translator reads from disk without LLM
	tr2 := New(st, nil, slog.Default())
	if got := tr2.Name("Ladislav Krejcí"); got != "克雷伊奇" {
		t.Errorf("persisted = %q", got)
	}
	// nil receiver is safe (watcher without translator)
	var nilT *Translator
	if got := nilT.Name("X"); got != "X" {
		t.Errorf("nil = %q", got)
	}
}
