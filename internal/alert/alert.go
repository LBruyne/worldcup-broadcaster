// Package alert notifies the admin QQ about runtime failures, rate-limited
// per category so an outage does not flood their inbox.
package alert

import (
	"fmt"
	"log/slog"
	"sync"
	"time"
)

type Notifier interface {
	SendPrivate(userID int64, msg string) error
}

type Alerter struct {
	notifier Notifier
	adminQQ  int64
	logger   *slog.Logger
	cooldown time.Duration

	mu   sync.Mutex
	last map[string]time.Time

	now func() time.Time // overridable in tests
}

func New(n Notifier, adminQQ int64, logger *slog.Logger) *Alerter {
	return &Alerter{
		notifier: n,
		adminQQ:  adminQQ,
		logger:   logger,
		cooldown: 30 * time.Minute,
		last:     make(map[string]time.Time),
		now:      time.Now,
	}
}

// Alert logs the problem and, at most once per cooldown per category, sends
// a private QQ message to the admin.
func (a *Alerter) Alert(category, msg string) {
	a.logger.Error("ALERT", "category", category, "message", msg)
	if a.adminQQ == 0 {
		return
	}
	a.mu.Lock()
	now := a.now()
	if t, ok := a.last[category]; ok && now.Sub(t) < a.cooldown {
		a.mu.Unlock()
		return
	}
	a.last[category] = now
	a.mu.Unlock()

	text := fmt.Sprintf("⚠️ 世界杯Bot告警 [%s]\n%s\n时间: %s", category, msg, now.Format("2006-01-02 15:04:05"))
	if err := a.notifier.SendPrivate(a.adminQQ, text); err != nil {
		a.logger.Error("failed to deliver alert to admin", "error", err)
	}
}
