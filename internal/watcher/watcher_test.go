package watcher

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"worldcup-broadcaster/internal/config"
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

func (f *fakeSender) all() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.msgs...)
}

// recentKickoff returns an ESPN-format date one minute in the past so the
// MaxDuration guard never fires during tests.
func recentKickoff() string {
	return time.Now().UTC().Add(-time.Minute).Format("2006-01-02T15:04Z")
}

func allEvents() config.Events {
	return config.Events{
		Kickoff: true, Goal: true, YellowCard: true, RedCard: true,
		Halftime: true, SecondHalfStart: true, Substitution: true,
		PeriodChange: true, ShootoutRound: true, Fulltime: true,
	}
}

// snapshotAt clones the final-match fixture truncated to n key events, with
// live status and a score as of that point.
func snapshotAt(t *testing.T, full *espn.Summary, n int, state, homeScore, awayScore string, withShootout bool) []byte {
	t.Helper()
	snap := *full
	snap.KeyEvents = full.KeyEvents[:n]
	if !withShootout {
		snap.Shootout = nil
	}
	hc := full.Header.Competitions[0]
	hc.Status.Type.State = state
	if state == "in" {
		hc.Status.Type.Detail = "In Progress"
	}
	comps := make([]espn.Competitor, len(hc.Competitors))
	copy(comps, hc.Competitors)
	for i := range comps {
		if state == "in" {
			comps[i].ShootoutScore = 0
			if comps[i].HomeAway == "home" {
				comps[i].Score = homeScore
			} else {
				comps[i].Score = awayScore
			}
		}
	}
	hc.Competitors = comps
	snap.Header = espn.Header{Competitions: []espn.HeaderCompetition{hc}}
	raw, err := json.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestWatchReplaysFullMatch(t *testing.T) {
	full := loadFinal(t)
	snaps := [][]byte{
		snapshotAt(t, full, 0, "in", "0", "0", false),   // pre/early, nothing yet
		snapshotAt(t, full, 2, "in", "1", "0", false),   // kickoff + messi pen
		snapshotAt(t, full, 5, "in", "2", "0", false),   // + goal + 2 subs
		snapshotAt(t, full, 37, "post", "3", "3", true), // everything incl. shootout + end
	}
	var idx atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		i := int(idx.Add(1)) - 1
		if i >= len(snaps) {
			i = len(snaps) - 1
		}
		w.Write(snaps[i])
	}))
	defer srv.Close()

	sender := &fakeSender{}
	st := store.New(t.TempDir())
	var alerts []string
	w := New(espn.NewClientWithBase(srv.URL, "fifa.world"), sender, st,
		func(cat, msg string) { alerts = append(alerts, cat+": "+msg) },
		slog.Default(),
		Options{PollInterval: 5 * time.Millisecond, PreKickoff: 0, MaxDuration: time.Hour, Events: allEvents()})

	ev := espn.Event{ID: "633850", Date: recentKickoff(), ShortName: "FRA @ ARG"}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := w.Watch(ctx, ev); err != nil {
		t.Fatalf("Watch: %v", err)
	}

	msgs := sender.all()
	// 37 key events + 8 shootout rounds = 45 broadcasts
	if len(msgs) != 45 {
		t.Fatalf("messages = %d, want 45\nfirst: %s", len(msgs), msgs[0])
	}
	if !strings.Contains(msgs[0], "比赛开始") {
		t.Errorf("msg0 = %s", msgs[0])
	}
	// Messi's 23' penalty must carry the 1:0 scoreline from its own text.
	if !strings.Contains(msgs[1], "点球命中") || !strings.Contains(msgs[1], "1 : 0") {
		t.Errorf("msg1 = %s", msgs[1])
	}
	last := msgs[len(msgs)-1]
	if !strings.Contains(last, "全场结束") || !strings.Contains(last, "点球 4:2") || !strings.Contains(last, "今晚几个") {
		t.Errorf("last = %s", last)
	}
	if len(alerts) != 0 {
		t.Errorf("unexpected alerts: %v", alerts)
	}
}

func TestWatchRestartDoesNotRepush(t *testing.T) {
	full := loadFinal(t)
	finalSnap := snapshotAt(t, full, 37, "post", "3", "3", true)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(finalSnap)
	}))
	defer srv.Close()

	dir := t.TempDir()
	ev := espn.Event{ID: "633850", Date: recentKickoff(), ShortName: "FRA @ ARG"}
	opts := Options{PollInterval: 5 * time.Millisecond, PreKickoff: 0, MaxDuration: time.Hour, Events: allEvents()}

	run := func() []string {
		sender := &fakeSender{}
		w := New(espn.NewClientWithBase(srv.URL, "fifa.world"), sender, store.New(dir),
			func(string, string) {}, slog.Default(), opts)
		if err := w.Watch(context.Background(), ev); err != nil {
			t.Fatalf("Watch: %v", err)
		}
		return sender.all()
	}

	first := run()
	if len(first) != 45 {
		t.Fatalf("first run = %d, want 45", len(first))
	}
	second := run() // simulated restart on a finished match
	if len(second) != 0 {
		t.Fatalf("restart re-pushed %d messages: %q", len(second), second[0])
	}
}

func TestWatchAlertsOnRepeatedFailures(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	var mu sync.Mutex
	var alerts []string
	w := New(espn.NewClientWithBase(srv.URL, "fifa.world"), &fakeSender{}, store.New(t.TempDir()),
		func(cat, msg string) { mu.Lock(); alerts = append(alerts, cat); mu.Unlock() },
		slog.Default(),
		Options{PollInterval: time.Millisecond, PreKickoff: 0, MaxDuration: time.Hour, Events: allEvents()})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Watch(ctx, espn.Event{ID: "x", Date: recentKickoff()}) }()

	deadline := time.Now().Add(15 * time.Second)
	for {
		mu.Lock()
		n := len(alerts)
		mu.Unlock()
		if n > 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
	mu.Lock()
	defer mu.Unlock()
	if len(alerts) == 0 || alerts[0] != "espn" {
		t.Fatalf("alerts = %v, want espn alert after 5 consecutive failures", alerts)
	}
}

// Events disabled in config are suppressed but still marked, so toggling a
// switch later never backfills stale events.
func TestWatchConfigSuppression(t *testing.T) {
	full := loadFinal(t)
	finalSnap := snapshotAt(t, full, 37, "post", "3", "3", true)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(finalSnap)
	}))
	defer srv.Close()

	evs := allEvents()
	evs.Substitution = false
	evs.YellowCard = false
	sender := &fakeSender{}
	w := New(espn.NewClientWithBase(srv.URL, "fifa.world"), sender, store.New(t.TempDir()),
		func(string, string) {}, slog.Default(),
		Options{PollInterval: 5 * time.Millisecond, PreKickoff: 0, MaxDuration: time.Hour, Events: evs})
	if err := w.Watch(context.Background(), espn.Event{ID: "633850", Date: recentKickoff()}); err != nil {
		t.Fatal(err)
	}
	msgs := sender.all()
	// 45 - 13 subs - 8 yellows = 24
	if len(msgs) != 24 {
		t.Fatalf("messages = %d, want 24", len(msgs))
	}
	for _, m := range msgs {
		if strings.Contains(m, "🔄") || strings.Contains(m, "🟨") {
			t.Errorf("suppressed event leaked: %s", m)
		}
	}
}
