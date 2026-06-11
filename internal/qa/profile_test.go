package qa

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"worldcup-broadcaster/internal/store"
)

func TestProfilesSeedAndPersist(t *testing.T) {
	dir := t.TempDir()
	st := store.New(dir)
	seeds := map[string]string{"群主哥": "群主，曼联球迷"}
	p := NewProfiles(st, nil, seeds, slog.Default())
	if p.Persona("群主哥") != "群主，曼联球迷" {
		t.Errorf("seed persona = %q", p.Persona("群主哥"))
	}
	p.SetPersona("小红", "阿森纳球迷")
	p.Record(context.Background(), "小红", "今天枪手赢了", nil)

	// reload from disk
	p2 := NewProfiles(store.New(dir), nil, seeds, slog.Default())
	if p2.Persona("小红") != "阿森纳球迷" {
		t.Errorf("persisted persona = %q", p2.Persona("小红"))
	}
	if p2.Persona("群主哥") != "群主，曼联球迷" {
		t.Error("seed lost after reload")
	}
}

func TestProfilesRefreshAfterEnoughMessages(t *testing.T) {
	st := store.New(t.TempDir())
	llm := &fakeLLM{reply: "话痨，皇马球迷，爱抬杠"}
	p := NewProfiles(st, llm, nil, slog.Default())
	for i := 0; i < profileRefreshEvery; i++ {
		p.Record(context.Background(), "话痨哥", "皇马天下第一", []ChatMsg{{Nickname: "话痨哥", Text: "皇马天下第一"}})
	}
	deadline := 100
	for p.Persona("话痨哥") == "" && deadline > 0 {
		deadline--
		// refresh runs async
		if deadline == 0 {
			t.Fatal("persona not refreshed")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := p.Persona("话痨哥"); got != "话痨，皇马球迷，爱抬杠" {
		t.Errorf("persona = %q", got)
	}
}

func TestKnownFiltersByChat(t *testing.T) {
	p := NewProfiles(store.New(t.TempDir()), nil, map[string]string{"铁哥": "C罗粉"}, slog.Default())
	known := p.Known([]ChatMsg{{Nickname: "铁哥", Text: "x"}, {Nickname: "路人", Text: "y"}})
	if len(known) != 1 || known["铁哥"] != "C罗粉" {
		t.Errorf("known = %v", known)
	}
}
