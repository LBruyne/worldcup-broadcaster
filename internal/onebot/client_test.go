package onebot

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type recorded struct {
	path string
	auth string
	body map[string]any
	at   time.Time
}

func fakeNapCat(t *testing.T, fail bool) (*httptest.Server, *[]recorded, *sync.Mutex) {
	t.Helper()
	var mu sync.Mutex
	var recs []recorded
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		json.Unmarshal(raw, &body)
		mu.Lock()
		recs = append(recs, recorded{r.URL.Path, r.Header.Get("Authorization"), body, time.Now()})
		mu.Unlock()
		if fail {
			w.Write([]byte(`{"status":"failed","retcode":1400,"message":"not logged in"}`))
			return
		}
		w.Write([]byte(`{"status":"ok","retcode":0}`))
	}))
	t.Cleanup(srv.Close)
	return srv, &recs, &mu
}

func TestSendGroupNow(t *testing.T) {
	srv, recs, mu := fakeNapCat(t, false)
	c := New(srv.URL, "secret-token", []int64{794925183}, time.Millisecond, slog.Default())
	if err := c.sendGroup(794925183, "⚽ test"); err != nil {
		t.Fatalf("sendGroup: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	r := (*recs)[0]
	if r.path != "/send_group_msg" {
		t.Errorf("path = %s", r.path)
	}
	if r.auth != "Bearer secret-token" {
		t.Errorf("auth = %s", r.auth)
	}
	if r.body["group_id"].(float64) != 794925183 || r.body["message"] != "⚽ test" {
		t.Errorf("body = %v", r.body)
	}
}

func TestQueueSerialWithInterval(t *testing.T) {
	srv, recs, mu := fakeNapCat(t, false)
	interval := 50 * time.Millisecond
	c := New(srv.URL, "", []int64{123}, interval, slog.Default())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c.Start(ctx)

	c.EnqueueGroup("msg1")
	c.EnqueueGroup("msg2")
	c.EnqueueGroup("msg3")

	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		n := len(*recs)
		mu.Unlock()
		if n == 3 || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(*recs) != 3 {
		t.Fatalf("sent = %d, want 3", len(*recs))
	}
	for i := 1; i < 3; i++ {
		gap := (*recs)[i].at.Sub((*recs)[i-1].at)
		if gap < interval {
			t.Errorf("gap %d = %v, want >= %v", i, gap, interval)
		}
	}
	if (*recs)[0].body["message"] != "msg1" || (*recs)[2].body["message"] != "msg3" {
		t.Error("messages out of order")
	}
}

func TestSendErrorReported(t *testing.T) {
	srv, _, _ := fakeNapCat(t, true)
	c := New(srv.URL, "", []int64{123}, time.Millisecond, slog.Default())
	var gotErr error
	var wg sync.WaitGroup
	wg.Add(1)
	c.OnSendError = func(err error) { gotErr = err; wg.Done() }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c.Start(ctx)
	c.EnqueueGroup("will fail")
	wg.Wait()
	if gotErr == nil {
		t.Fatal("expected send error to be reported")
	}
}

func TestLogOnlyModeWithoutGroup(t *testing.T) {
	c := New("http://127.0.0.1:1", "", nil, time.Millisecond, slog.Default())
	c.EnqueueGroup("no group configured") // must not panic or enqueue
	if len(c.queue) != 0 {
		t.Fatal("log-only mode must not enqueue")
	}
}

func TestSendPrivate(t *testing.T) {
	srv, recs, mu := fakeNapCat(t, false)
	c := New(srv.URL, "", []int64{123}, time.Millisecond, slog.Default())
	if err := c.SendPrivate(10001, "alert!"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	r := (*recs)[0]
	if r.path != "/send_private_msg" || r.body["user_id"].(float64) != 10001 {
		t.Errorf("private call = %+v", r)
	}
}

func TestGetStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/get_status" {
			t.Errorf("path = %s", r.URL.Path)
		}
		w.Write([]byte(`{"status":"ok","retcode":0,"data":{"online":true,"good":true}}`))
	}))
	defer srv.Close()
	c := New(srv.URL, "", []int64{1}, time.Millisecond, slog.Default())
	online, err := c.GetStatus()
	if err != nil || !online {
		t.Fatalf("online=%v err=%v", online, err)
	}

	down := New("http://127.0.0.1:1", "", []int64{1}, time.Millisecond, slog.Default())
	if _, err := down.GetStatus(); err == nil {
		t.Fatal("expected error when napcat unreachable")
	}
}

func TestSplitMessage(t *testing.T) {
	if got := splitMessage("short", 100); len(got) != 1 || got[0] != "short" {
		t.Errorf("short = %v", got)
	}
	long := strings.Repeat("第一行内容\n", 100) // 600 runes
	chunks := splitMessage(strings.TrimRight(long, "\n"), 100)
	if len(chunks) != 7 {
		t.Fatalf("chunks = %d", len(chunks))
	}
	for i, c := range chunks {
		if n := len([]rune(c)); n > 100 {
			t.Errorf("chunk %d = %d runes", i, n)
		}
		if strings.HasPrefix(c, "\n") || strings.HasSuffix(c, "\n") {
			t.Errorf("chunk %d has dangling newline: %q", i, c)
		}
	}
	if joined := strings.Join(chunks, "\n"); joined != strings.TrimRight(long, "\n") {
		t.Error("chunks do not reassemble to original")
	}
	// a single line longer than max is hard-split
	hard := splitMessage(strings.Repeat("a", 250), 100)
	if len(hard) != 3 {
		t.Errorf("hard split = %d", len(hard))
	}
}

func TestPaceForScales(t *testing.T) {
	c := &Client{interval: 3 * time.Second}
	short := c.paceFor(10)
	long := c.paceFor(600)
	if short < 2*time.Second || short > 5*time.Second {
		t.Errorf("short pace out of range: %v", short)
	}
	if long <= short {
		t.Errorf("long (%v) must exceed short (%v)", long, short)
	}
	if long > 25*time.Second {
		t.Errorf("pace not capped: %v", long)
	}
	// tiny interval (test mode) stays tiny even for long text
	ct := &Client{interval: 10 * time.Millisecond}
	if d := ct.paceFor(600); d > 200*time.Millisecond {
		t.Errorf("tiny-interval pace too large: %v", d)
	}
}

func TestPendingHoldAndFlush(t *testing.T) {
	c := New("http://x", "", []int64{1}, time.Millisecond, slog.Default())
	c.holdPending(111, "msg-a")
	c.holdPending(111, "msg-b")
	if c.PendingCount() != 2 {
		t.Fatalf("pending = %d", c.PendingCount())
	}
	// drain via the queue so FlushPending re-enqueues without a live server
	c.queue = make(chan groupMsg, 10)
	n := c.FlushPending(time.Hour)
	if n != 2 {
		t.Errorf("flushed = %d, want 2", n)
	}
	if c.PendingCount() != 0 {
		t.Errorf("pending not cleared: %d", c.PendingCount())
	}
	got := <-c.queue
	if got.text != "msg-a" {
		t.Errorf("order broken: %q", got.text)
	}
}

func TestFlushPendingDropsStale(t *testing.T) {
	c := New("http://x", "", []int64{1}, time.Millisecond, slog.Default())
	c.pending = []PendingMsg{
		{GroupID: 1, Text: "old", At: time.Now().Add(-5 * time.Hour)},
		{GroupID: 1, Text: "fresh", At: time.Now()},
	}
	c.queue = make(chan groupMsg, 10)
	if n := c.FlushPending(3 * time.Hour); n != 1 {
		t.Errorf("flushed = %d, want 1 (stale dropped)", n)
	}
}

func TestParseHistory(t *testing.T) {
	body := []byte(`{"data":{"messages":[
		{"time":100,"sender":{"user_id":5,"nickname":"小明"},"message":[{"type":"text","data":{"text":"/ask 谁会赢"}}]},
		{"time":200,"sender":{"user_id":6,"nickname":"小红","card":"红姐"},"message":[{"type":"at","data":{"qq":"9"}},{"type":"text","data":{"text":" 在吗"}}]}]}}`)
	msgs, err := parseHistory(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 {
		t.Fatalf("msgs = %d", len(msgs))
	}
	if msgs[0].Nickname != "小明" || msgs[0].Text != "/ask 谁会赢" || msgs[0].Time != 100 {
		t.Errorf("msg0 = %+v", msgs[0])
	}
	if msgs[1].Nickname != "红姐" { // card preferred
		t.Errorf("card not preferred: %+v", msgs[1])
	}
}
