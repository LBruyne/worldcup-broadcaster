// Package e2e wires real components together (onebot HTTP client + queue,
// llm HTTP client, digest, watcher, store) against fake NapCat / ESPN /
// DeepSeek servers and verifies the full broadcast chain.
package e2e

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"worldcup-broadcaster/internal/config"
	"worldcup-broadcaster/internal/digest"
	"worldcup-broadcaster/internal/espn"
	"worldcup-broadcaster/internal/llm"
	"worldcup-broadcaster/internal/onebot"
	"worldcup-broadcaster/internal/store"
	"worldcup-broadcaster/internal/watcher"
)

type napcat struct {
	mu   sync.Mutex
	msgs []string
	srv  *httptest.Server
}

func newNapCat(t *testing.T, wantToken string) *napcat {
	n := &napcat{}
	n.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/send_group_msg" {
			http.NotFound(w, r)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+wantToken {
			t.Errorf("auth = %q", got)
		}
		raw, _ := io.ReadAll(r.Body)
		var body struct {
			GroupID int64  `json:"group_id"`
			Message string `json:"message"`
		}
		json.Unmarshal(raw, &body)
		if body.GroupID != 654321 {
			t.Errorf("group = %d", body.GroupID)
		}
		n.mu.Lock()
		n.msgs = append(n.msgs, body.Message)
		n.mu.Unlock()
		w.Write([]byte(`{"status":"ok","retcode":0}`))
	}))
	t.Cleanup(n.srv.Close)
	return n
}

func (n *napcat) count() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return len(n.msgs)
}

func (n *napcat) joined() string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return strings.Join(n.msgs, "\n###\n")
}

func (n *napcat) waitFor(t *testing.T, want int) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for n.count() < want {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %d messages, have %d", want, n.count())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile("../testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// TestFullChainPreview drives: config load -> digest.Preview -> llm (fake
// DeepSeek over HTTP) -> onebot queue -> fake NapCat.
func TestFullChainPreview(t *testing.T) {
	nap := newNapCat(t, "tok123")

	espnSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "scoreboard") {
			w.Write(fixture(t, "scoreboard-upcoming.json"))
			return
		}
		w.Write(fixture(t, "summary-upcoming-760415.json"))
	}))
	defer espnSrv.Close()

	deepseek := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(raw), "deepseek-v4-pro") {
			t.Errorf("model missing in request: %.200s", raw)
		}
		w.Write([]byte(`{"choices":[{"message":{"content":"🔸 看点来咯：开幕战阿兹特克见！"}}]}`))
	}))
	defer deepseek.Close()

	// Real config file exercising the Load path.
	cfgFile := t.TempDir() + "/config.yaml"
	os.WriteFile(cfgFile, []byte(`
onebot:
  base_url: "`+nap.srv.URL+`"
  access_token: "tok123"
  group_id: 654321
  send_interval_ms: 10
llm:
  base_url: "`+deepseek.URL+`"
  api_key: "sk-e2e"
data_dir: "`+t.TempDir()+`"
log:
  dir: ""
`), 0o644)
	cfg, err := config.Load(cfgFile)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	bot := onebot.New(cfg.OneBot.BaseURL, cfg.OneBot.AccessToken, cfg.OneBot.GroupID,
		time.Duration(cfg.OneBot.SendIntervalMS)*time.Millisecond, logger)
	bot.Start(ctx)
	st := store.New(cfg.DataDir)
	llmClient := llm.New(cfg.LLM.BaseURL, cfg.LLM.APIKey, cfg.LLM.Model,
		time.Duration(cfg.LLM.TimeoutSec)*time.Second)
	dig := digest.New(espn.NewClientWithBase(espnSrv.URL, cfg.ESPN.League), llmClient, st, bot,
		func(string, string) {}, logger)

	if err := dig.Preview(ctx, "2026-06-12"); err != nil {
		t.Fatal(err)
	}
	nap.waitFor(t, 1)
	got := nap.joined()
	for _, want := range []string{"🇲🇽墨西哥 vs 南非🇿🇦", "北京时间 03:00", "看点来咯"} {
		if !strings.Contains(got, want) {
			t.Errorf("napcat missing %q\n%s", want, got)
		}
	}
}

// TestFullChainLiveMatch replays the 2022 final through the real onebot
// queue and verifies ordered delivery and restart dedup.
func TestFullChainLiveMatch(t *testing.T) {
	nap := newNapCat(t, "tok123")

	full := fixture(t, "summary-633850.json")
	var sum espn.Summary
	if err := json.Unmarshal(full, &sum); err != nil {
		t.Fatal(err)
	}
	espnSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(full) // final state: post, all events
	}))
	defer espnSrv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	bot := onebot.New(nap.srv.URL, "tok123", 654321, time.Millisecond, logger)
	bot.Start(ctx)
	dir := t.TempDir()

	evs := config.Events{Kickoff: true, Goal: true, YellowCard: true, RedCard: true,
		Halftime: true, SecondHalfStart: true, Substitution: true,
		PeriodChange: true, ShootoutRound: true, Fulltime: true}
	mkWatcher := func() *watcher.Watcher {
		return watcher.New(espn.NewClientWithBase(espnSrv.URL, "fifa.world"), bot, store.New(dir),
			func(string, string) {}, logger,
			watcher.Options{PollInterval: 5 * time.Millisecond, PreKickoff: 0, MaxDuration: time.Hour, Events: evs})
	}
	ev := espn.Event{ID: "633850",
		Date:      time.Now().UTC().Add(-time.Minute).Format("2006-01-02T15:04Z"),
		ShortName: "FRA @ ARG"}

	if err := mkWatcher().Watch(ctx, ev); err != nil {
		t.Fatal(err)
	}
	nap.waitFor(t, 45)
	got := nap.joined()
	if !strings.Contains(got, "GOOOOOAL") || !strings.Contains(got, "点球 4:2") {
		t.Errorf("live chain output wrong:\n%.500s", got)
	}

	// restart: same store dir, finished match -> zero new sends
	if err := mkWatcher().Watch(ctx, ev); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if nap.count() != 45 {
		t.Fatalf("restart re-pushed: %d messages", nap.count())
	}
}
