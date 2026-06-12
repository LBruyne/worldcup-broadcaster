// Package alert notifies the admin QQ about runtime failures, rate-limited
// per category so an outage does not flood their inbox.
package alert

import (
	"fmt"
	"log/slog"
	"os"
	"os/exec"
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
	cmd      string     // optional fallback shell command (webhook etc.)
	text     TextSender // optional out-of-QQ text channel (DingTalk)

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

// SetCommand wires an external fallback channel: cmd runs via sh -c with
// ALERT_CATEGORY / ALERT_MESSAGE in its environment. Unlike the QQ private
// message, it still works while the QQ session itself is dead.
func (a *Alerter) SetCommand(cmd string) { a.cmd = cmd }

// TextSender is implemented by *dingtalk.Client.
type TextSender interface {
	SendText(text string) error
}

// SetTextSender wires an additional out-of-QQ alert channel (DingTalk).
func (a *Alerter) SetTextSender(s TextSender) { a.text = s }

// Alert logs the problem and, at most once per cooldown per category,
// notifies the admin via QQ private message and the fallback command.
func (a *Alerter) Alert(category, msg string) {
	a.logger.Error("ALERT", "category", category, "message", msg)
	if a.adminQQ == 0 && a.cmd == "" {
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
	if a.adminQQ != 0 {
		if err := a.notifier.SendPrivate(a.adminQQ, text); err != nil {
			a.logger.Error("failed to deliver alert to admin", "error", err)
		}
	}
	if a.cmd != "" {
		go func() {
			c := exec.Command("sh", "-c", a.cmd)
			c.Env = append(os.Environ(), "ALERT_CATEGORY="+category, "ALERT_MESSAGE="+text)
			if out, err := c.CombinedOutput(); err != nil {
				a.logger.Error("alert command failed", "error", err, "output", string(out))
			}
		}()
	}
	if a.text != nil {
		go func() {
			if err := a.text.SendText(text); err != nil {
				a.logger.Error("dingtalk alert failed", "error", err)
			}
		}()
	}
}
