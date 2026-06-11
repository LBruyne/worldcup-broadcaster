package onebot

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
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
	c := New(srv.URL, "secret-token", 794925183, time.Millisecond, slog.Default())
	if err := c.SendGroupNow("⚽ test"); err != nil {
		t.Fatalf("SendGroupNow: %v", err)
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
	c := New(srv.URL, "", 123, interval, slog.Default())
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
	c := New(srv.URL, "", 123, time.Millisecond, slog.Default())
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
	c := New("http://127.0.0.1:1", "", 0, time.Millisecond, slog.Default())
	if err := c.SendGroupNow("no group configured"); err != nil {
		t.Fatalf("log-only mode should not error: %v", err)
	}
}

func TestSendPrivate(t *testing.T) {
	srv, recs, mu := fakeNapCat(t, false)
	c := New(srv.URL, "", 123, time.Millisecond, slog.Default())
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
