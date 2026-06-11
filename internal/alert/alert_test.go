package alert

import (
	"log/slog"
	"testing"
	"time"
)

type fakeNotifier struct {
	sent []string
}

func (f *fakeNotifier) SendPrivate(userID int64, msg string) error {
	f.sent = append(f.sent, msg)
	return nil
}

func TestRateLimitPerCategory(t *testing.T) {
	n := &fakeNotifier{}
	a := New(n, 10001, slog.Default())
	clock := time.Date(2026, 6, 12, 3, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return clock }

	a.Alert("espn", "fetch failed x5")
	a.Alert("espn", "fetch failed x6") // suppressed
	a.Alert("onebot", "send failed")   // different category, goes through
	if len(n.sent) != 2 {
		t.Fatalf("sent = %d, want 2", len(n.sent))
	}

	clock = clock.Add(31 * time.Minute)
	a.Alert("espn", "still failing")
	if len(n.sent) != 3 {
		t.Fatalf("after cooldown sent = %d, want 3", len(n.sent))
	}
}

func TestNoAdminConfigured(t *testing.T) {
	n := &fakeNotifier{}
	a := New(n, 0, slog.Default())
	a.Alert("espn", "boom")
	if len(n.sent) != 0 {
		t.Fatal("should not send when admin_qq is 0")
	}
}
