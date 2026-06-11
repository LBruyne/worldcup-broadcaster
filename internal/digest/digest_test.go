package digest

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"worldcup-broadcaster/internal/espn"
	"worldcup-broadcaster/internal/store"
)

type fakeSender struct {
	mu   sync.Mutex
	msgs []string
}

func (f *fakeSender) EnqueueGroup(msg string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.msgs = append(f.msgs, msg)
}

func (f *fakeSender) joined() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return strings.Join(f.msgs, "\n<<<SPLIT>>>\n")
}

type fakeLLM struct {
	reply   string
	err     error
	gotUser string
}

func (f *fakeLLM) Generate(_ context.Context, system, user string) (string, error) {
	f.gotUser = user
	if f.err != nil {
		return "", f.err
	}
	return f.reply, nil
}

// fakeESPN serves recorded fixtures: the given scoreboard for any scoreboard
// request, and per-id summaries.
func fakeESPN(t *testing.T, scoreboardFile string, summaries map[string]string) *espn.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var file string
		if strings.Contains(r.URL.Path, "scoreboard") {
			file = scoreboardFile
		} else {
			file = summaries[r.URL.Query().Get("event")]
		}
		if file == "" {
			http.NotFound(w, r)
			return
		}
		raw, err := os.ReadFile("../../testdata/" + file)
		if err != nil {
			t.Errorf("fixture %s: %v", file, err)
			http.Error(w, "missing", 500)
			return
		}
		w.Write(raw)
	}))
	t.Cleanup(srv.Close)
	return espn.NewClientWithBase(srv.URL, "fifa.world")
}

func TestPreview(t *testing.T) {
	client := fakeESPN(t, "scoreboard-upcoming.json", map[string]string{
		"760415": "summary-upcoming-760415.json",
		"760414": "summary-upcoming-760415.json",
	})
	sender := &fakeSender{}
	llm := &fakeLLM{reply: "🔸 墨西哥 vs 南非：开幕战必看！主场气势如虹。"}
	st := store.New(t.TempDir())
	d := New(client, llm, st, sender, func(string, string) {}, slog.Default())

	if err := d.Preview(context.Background(), "2026-06-12"); err != nil {
		t.Fatalf("Preview: %v", err)
	}
	got := sender.joined()
	for _, want := range []string{
		"6月12日 周五", "共2场",
		"🇲🇽墨西哥 vs 南非🇿🇦", "北京时间 03:00",
		"🇰🇷韩国 vs 捷克🇨🇿", "北京时间 10:00",
		"小组赛", "Estadio Banorte",
		"近况", "近2次交锋",
		"⭐ 明日看点", "开幕战必看",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("preview missing %q\n----\n%s", want, got)
		}
	}
	// LLM must receive the structured data
	if !strings.Contains(llm.gotUser, `"head_to_head"`) || !strings.Contains(llm.gotUser, "Mexico") {
		t.Errorf("llm prompt missing data: %.300s", llm.gotUser)
	}
	// schedule persisted
	var saved []previewMatch
	if err := st.LoadJSON("2026-06-12", "schedule", &saved); err != nil || len(saved) != 2 {
		t.Errorf("persisted schedule = %v, err %v", saved, err)
	}
}

func TestPreviewDegradesWithoutLLM(t *testing.T) {
	client := fakeESPN(t, "scoreboard-upcoming.json", map[string]string{
		"760415": "summary-upcoming-760415.json",
		"760414": "summary-upcoming-760415.json",
	})
	sender := &fakeSender{}
	var alerts []string
	d := New(client, &fakeLLM{err: errors.New("insufficient balance")}, store.New(t.TempDir()),
		sender, func(cat, msg string) { alerts = append(alerts, cat) }, slog.Default())

	if err := d.Preview(context.Background(), "2026-06-12"); err != nil {
		t.Fatalf("Preview must not fail when llm fails: %v", err)
	}
	got := sender.joined()
	if !strings.Contains(got, "🇲🇽墨西哥 vs 南非🇿🇦") {
		t.Errorf("base preview missing:\n%s", got)
	}
	if strings.Contains(got, "明日看点") {
		t.Error("highlights section should be absent on llm failure")
	}
	if len(alerts) != 1 || alerts[0] != "llm" {
		t.Errorf("alerts = %v", alerts)
	}
}

func TestRecapFinalWithShootout(t *testing.T) {
	client := fakeESPN(t, "scoreboard-20221218.json", map[string]string{
		"633850": "summary-633850.json",
	})
	sender := &fakeSender{}
	llm := &fakeLLM{reply: "梅西封王！点球大战看到心脏骤停。"}
	st := store.New(t.TempDir())
	d := New(client, llm, st, sender, func(string, string) {}, slog.Default())

	if err := d.Recap(context.Background(), "2022-12-18"); err != nil {
		t.Fatalf("Recap: %v", err)
	}
	got := sender.joined()
	for _, want := range []string{
		"战报", "决赛",
		"🇦🇷阿根廷 3 : 3 法国🇫🇷（点球 4:2）",
		"23' Lionel Messi（点球）",
		"36' Ángel Di María", "助攻:Alexis Mac Allister",
		"🎙️ 今日锐评", "梅西封王",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("recap missing %q\n----\n%s", want, got)
		}
	}
	var saved []recapMatch
	if err := st.LoadJSON("2022-12-18", "recap", &saved); err != nil || len(saved) != 1 {
		t.Fatalf("persisted recap = %v, err %v", saved, err)
	}
	if len(saved[0].Goals) != 6 {
		t.Errorf("goals = %d, want 6", len(saved[0].Goals))
	}
}

func TestRecapDegradesWithoutLLM(t *testing.T) {
	client := fakeESPN(t, "scoreboard-20221218.json", map[string]string{
		"633850": "summary-633850.json",
	})
	sender := &fakeSender{}
	d := New(client, &fakeLLM{err: errors.New("timeout")}, store.New(t.TempDir()),
		sender, func(string, string) {}, slog.Default())
	if err := d.Recap(context.Background(), "2022-12-18"); err != nil {
		t.Fatalf("Recap must not fail when llm fails: %v", err)
	}
	got := sender.joined()
	if !strings.Contains(got, "3 : 3") || strings.Contains(got, "今日锐评") {
		t.Errorf("degraded recap wrong:\n%s", got)
	}
}

func TestSendSplitLongMessage(t *testing.T) {
	sender := &fakeSender{}
	d := &Digest{sender: sender, logger: slog.Default()}
	block := strings.Repeat("好", 1200)
	text := "头部" + blockSep + block + blockSep + block + blockSep + block
	d.sendSplit(text)
	sender.mu.Lock()
	defer sender.mu.Unlock()
	if len(sender.msgs) < 2 {
		t.Fatalf("long digest not split: %d messages", len(sender.msgs))
	}
	for i, m := range sender.msgs {
		if n := len([]rune(m)); n > maxMessageRunes {
			t.Errorf("part %d = %d runes, exceeds bound", i, n)
		}
	}
}
