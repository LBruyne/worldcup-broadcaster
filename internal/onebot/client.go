// Package onebot sends QQ messages through a OneBot 11 HTTP endpoint
// (NapCat). Group messages flow through a serial queue with a minimum
// interval between sends to avoid QQ rate-limit penalties.
package onebot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net/http"
	"strings"
	"sync"
	"time"
)

type groupMsg struct {
	groupID int64
	text    string
}

// PendingMsg is a group send that failed (QQ offline) and is held for replay
// once the account is back online.
type PendingMsg struct {
	GroupID int64     `json:"group_id"`
	Text    string    `json:"text"`
	At      time.Time `json:"at"`
}

// pendingStore is the subset of *store.Store used to persist undelivered
// messages across restarts.
type pendingStore interface {
	LoadJSON(dir, name string, v any) error
	SaveJSON(dir, name string, v any) error
}

// HistMsg is one parsed group history entry.
type HistMsg struct {
	UserID   int64
	Nickname string
	Text     string
	Time     int64 // unix seconds
}

type Client struct {
	baseURL  string
	token    string
	groupIDs []int64
	interval time.Duration
	http     *http.Client
	logger   *slog.Logger

	queue chan groupMsg
	ctx   context.Context // set by Start; bounds in-flight HTTP on shutdown

	// mutedUntil tracks groups where sends recently failed with QQ result
	// 120 (bot muted / restricted): callers can check Muted to back off.
	mutedMu    sync.Mutex
	mutedUntil map[int64]time.Time

	// pending holds sends that failed while offline, for replay on recovery.
	pendMu  sync.Mutex
	pending []PendingMsg
	store   pendingStore

	// OnSendError, when set, is invoked for every failed group send so the
	// alerter can notify the admin.
	OnSendError func(error)
}

func New(baseURL, token string, groupIDs []int64, interval time.Duration, logger *slog.Logger) *Client {
	return &Client{
		baseURL:  baseURL,
		token:    token,
		groupIDs: groupIDs,
		interval: interval,
		// Long enough to outlive NapCat's internal 30s send-ack timeout.
		http:   &http.Client{Timeout: 35 * time.Second},
		logger: logger,
		// Sized for a worst-case burst: a restart during several concurrent
		// live matches can diff hundreds of events at once.
		queue:      make(chan groupMsg, 1024),
		mutedUntil: make(map[int64]time.Time),
	}
}

// Muted reports whether sends to a group recently failed with QQ's "muted"
// error (result 120). Cleared automatically after the backoff window.
func (c *Client) Muted(groupID int64) bool {
	c.mutedMu.Lock()
	defer c.mutedMu.Unlock()
	return time.Now().Before(c.mutedUntil[groupID])
}

func (c *Client) markMuted(groupID int64, d time.Duration) {
	c.mutedMu.Lock()
	c.mutedUntil[groupID] = time.Now().Add(d)
	c.mutedMu.Unlock()
	c.logger.Warn("group send rejected (likely muted), backing off", "group", groupID, "backoff", d)
}

// SetStore wires persistence for undelivered messages and loads any left
// over from a previous run.
func (c *Client) SetStore(s pendingStore) {
	c.store = s
	var saved []PendingMsg
	if s.LoadJSON("state", "pending-sends", &saved) == nil && len(saved) > 0 {
		c.pendMu.Lock()
		c.pending = saved
		c.pendMu.Unlock()
		c.logger.Info("loaded pending sends", "count", len(saved))
	}
}

// pace cap and tuning constants. The base comes from send_interval_ms; the
// length and burst components scale off it so one config knob tunes everything.
const (
	paceCap     = 30 * time.Second // a busy match must never stall indefinitely
	paceRunesPer = 40              // +1 base interval of "typing time" per N runes
	paceBurstMax = 3               // burst penalty saturates after this many back-to-back sends
)

// paceBase is the deterministic delay after a message (no jitter): a base
// interval, plus "typing time" scaling with length, plus an escalating penalty
// for back-to-back sends (a flurry of goals/cards/subs sent without the queue
// draining). Capped so a busy match can't stall forever.
func (c *Client) paceBase(runes, burst int) time.Duration {
	d := c.interval + time.Duration(int64(c.interval)*int64(runes)/paceRunesPer)
	if burst > paceBurstMax {
		burst = paceBurstMax
	}
	d += time.Duration(burst) * c.interval
	if d > paceCap {
		d = paceCap
	}
	return d
}

// paceFor adds ±25% jitter to paceBase so the cadence never looks mechanical to
// QQ risk control. burst is how many messages have been sent back-to-back
// without the queue draining (consecutive live events).
func (c *Client) paceFor(runes, burst int) time.Duration {
	jitter := 1.0 + (rand.Float64()*0.5 - 0.25)
	return time.Duration(float64(c.paceBase(runes, burst)) * jitter)
}

func (c *Client) sleep(ctx context.Context, d time.Duration) bool {
	select {
	case <-time.After(d):
		return true
	case <-ctx.Done():
		return false
	}
}

// Start launches the queue consumer; it drains until ctx is cancelled.
func (c *Client) Start(ctx context.Context) {
	c.ctx = ctx
	go func() {
		burst := 0 // consecutive back-to-back sends; widens the gap during flurries
		for {
			select {
			case <-ctx.Done():
				return
			case msg := <-c.queue:
				// Very long messages both trip NapCat's 30s send ack and
				// look like spam to QQ risk control: send them in chunks,
				// paced by length like a human typing.
				parts := splitMessage(msg.text, maxMsgRunes)
				for i, part := range parts {
					if i > 0 && !c.sleep(ctx, c.paceFor(len([]rune(parts[i-1])), burst)) {
						return
					}
					err := c.sendGroup(msg.groupID, part)
					if err != nil && strings.Contains(err.Error(), "Timeout: NTEvent") {
						// NapCat's send ack timing out does NOT mean the
						// message failed — it is usually already delivered.
						// Retrying duplicates it; treat as ambiguous success.
						c.logger.Warn("send ack timed out (likely delivered)", "group", msg.groupID)
						err = nil
					}
					if err != nil {
						if strings.Contains(err.Error(), `"result": 120`) {
							// muted: dropping is correct (replaying spams on unmute)
							c.markMuted(msg.groupID, 10*time.Minute)
						} else {
							// likely offline / transient: hold the un-sent
							// remainder for replay once back online.
							c.holdPending(msg.groupID, strings.Join(parts[i:], "\n"))
						}
						c.logger.Error("send group message failed", "error", err)
						if c.OnSendError != nil {
							go c.OnSendError(err)
						}
						break // stop this message; don't hammer a dead endpoint
					}
				}
				// A queue still holding messages means we're in a live flurry
				// (consecutive events): escalate the next gap. A drained queue
				// resets to calm pacing.
				if len(c.queue) > 0 {
					burst++
				} else {
					burst = 0
				}
				if !c.sleep(ctx, c.paceFor(len([]rune(parts[len(parts)-1])), burst)) {
					return
				}
			}
		}
	}()
}

// holdPending records an undelivered message for later replay (capped, with
// the oldest dropped past the cap).
func (c *Client) holdPending(groupID int64, text string) {
	c.pendMu.Lock()
	c.pending = append(c.pending, PendingMsg{GroupID: groupID, Text: text, At: time.Now()})
	if len(c.pending) > 200 {
		c.pending = c.pending[len(c.pending)-200:]
	}
	snapshot := append([]PendingMsg(nil), c.pending...)
	c.pendMu.Unlock()
	if c.store != nil {
		_ = c.store.SaveJSON("state", "pending-sends", snapshot)
	}
}

// PendingCount reports how many undelivered messages are queued for replay.
func (c *Client) PendingCount() int {
	c.pendMu.Lock()
	defer c.pendMu.Unlock()
	return len(c.pending)
}

// FlushPending re-enqueues messages that failed while offline (skipping any
// older than maxAge as too stale to matter) and clears the buffer. Returns
// the number re-enqueued.
func (c *Client) FlushPending(maxAge time.Duration) int {
	c.pendMu.Lock()
	pend := c.pending
	c.pending = nil
	c.pendMu.Unlock()
	if c.store != nil {
		_ = c.store.SaveJSON("state", "pending-sends", []PendingMsg{})
	}
	cutoff := time.Now().Add(-maxAge)
	n := 0
	for _, m := range pend {
		if m.At.Before(cutoff) {
			continue
		}
		c.EnqueueGroupTo(m.GroupID, m.Text)
		n++
	}
	if n > 0 {
		c.logger.Info("replaying pending sends", "count", n, "dropped_stale", len(pend)-n)
	}
	return n
}

// EnqueueGroup queues a broadcast to every configured group; drops (with a
// log) when the queue is saturated rather than blocking match watchers.
// In log-only mode (no groups configured) messages land in the log instead.
func (c *Client) EnqueueGroup(msg string) {
	if len(c.groupIDs) == 0 {
		c.logger.Warn("no group configured, message logged only", "message", msg)
		return
	}
	for _, gid := range c.groupIDs {
		c.EnqueueGroupTo(gid, msg)
	}
}

// EnqueueGroupTo queues a message for one specific group (QA replies go
// only to the group the command came from).
func (c *Client) EnqueueGroupTo(groupID int64, msg string) {
	select {
	case c.queue <- groupMsg{groupID, msg}:
	default:
		c.logger.Error("onebot queue full, dropping message", "group", groupID, "message", msg)
	}
}

// maxMsgRunes caps a single QQ group message; longer texts are chunked on
// line boundaries.
const maxMsgRunes = 1800

// splitMessage breaks text into chunks of at most max runes, preferring line
// boundaries; a single over-long line is hard-split.
func splitMessage(text string, max int) []string {
	if len([]rune(text)) <= max {
		return []string{text}
	}
	var chunks []string
	var cur []rune
	for _, line := range strings.Split(text, "\n") {
		r := []rune(line)
		for len(r) > max {
			if len(cur) > 0 {
				chunks = append(chunks, string(cur))
				cur = nil
			}
			chunks = append(chunks, string(r[:max]))
			r = r[max:]
		}
		need := len(r)
		if len(cur) > 0 {
			need += len(cur) + 1
		}
		if need > max {
			chunks = append(chunks, string(cur))
			cur = nil
		}
		if len(cur) > 0 {
			cur = append(cur, '\n')
		}
		cur = append(cur, r...)
	}
	if len(cur) > 0 {
		chunks = append(chunks, string(cur))
	}
	return chunks
}

func (c *Client) sendGroup(groupID int64, msg string) error {
	return c.call("/send_group_msg", map[string]any{
		"group_id": groupID,
		"message":  msg,
	})
}

// SendPrivate sends a private message (used for admin alerts), bypassing the
// queue so alerts are not delayed behind broadcast traffic.
func (c *Client) SendPrivate(userID int64, msg string) error {
	if userID == 0 {
		return nil
	}
	return c.call("/send_private_msg", map[string]any{
		"user_id": userID,
		"message": msg,
	})
}

// GroupMembers returns display names (card preferred) of a group's members.
func (c *Client) GroupMembers(groupID int64) ([]string, error) {
	raw, err := json.Marshal(map[string]any{"group_id": groupID})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodPost, c.baseURL+"/get_group_member_list", bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var r struct {
		Data []struct {
			Card     string `json:"card"`
			Nickname string `json:"nickname"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("member list parse: %.200s", body)
	}
	names := make([]string, 0, len(r.Data))
	for _, m := range r.Data {
		if m.Card != "" {
			names = append(names, m.Card)
		} else {
			names = append(names, m.Nickname)
		}
	}
	return names, nil
}

// GroupHistory returns up to count recent messages of a group (oldest first),
// used on recovery to find questions asked while the bot was offline.
func (c *Client) GroupHistory(groupID int64, count int) ([]HistMsg, error) {
	raw, err := json.Marshal(map[string]any{"group_id": groupID, "count": count})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodPost, c.baseURL+"/get_group_msg_history", bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	return parseHistory(body)
}

func parseHistory(body []byte) ([]HistMsg, error) {
	var r struct {
		Data struct {
			Messages []struct {
				Time   int64 `json:"time"`
				Sender struct {
					UserID   int64  `json:"user_id"`
					Nickname string `json:"nickname"`
					Card     string `json:"card"`
				} `json:"sender"`
				Message []struct {
					Type string `json:"type"`
					Data struct {
						Text string `json:"text"`
					} `json:"data"`
				} `json:"message"`
			} `json:"messages"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("history parse: %.200s", body)
	}
	out := make([]HistMsg, 0, len(r.Data.Messages))
	for _, m := range r.Data.Messages {
		var b strings.Builder
		for _, seg := range m.Message {
			if seg.Type == "text" {
				b.WriteString(seg.Data.Text)
			}
		}
		name := m.Sender.Card
		if name == "" {
			name = m.Sender.Nickname
		}
		out = append(out, HistMsg{UserID: m.Sender.UserID, Nickname: name, Text: b.String(), Time: m.Time})
	}
	return out, nil
}

// GetStatus reports whether the QQ account behind NapCat is online.
// An HTTP/transport error means NapCat itself is down.
func (c *Client) GetStatus() (online bool, err error) {
	raw, err := json.Marshal(map[string]any{})
	if err != nil {
		return false, err
	}
	req, err := http.NewRequest(http.MethodPost, c.baseURL+"/get_status", bytes.NewReader(raw))
	if err != nil {
		return false, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return false, fmt.Errorf("get_status: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return false, err
	}
	var r struct {
		Status string `json:"status"`
		Data   struct {
			Online bool `json:"online"`
			Good   bool `json:"good"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return false, fmt.Errorf("get_status parse: %q", body)
	}
	return r.Data.Online, nil
}

// WaitIdle blocks until the send queue drains (plus one interval for the
// in-flight message) or ctx expires. Used by one-shot CLI runs.
func (c *Client) WaitIdle(ctx context.Context) {
	for {
		if len(c.queue) == 0 {
			// Wait out a full pace window so a one-shot run doesn't exit while
			// the consumer is still pacing the final (possibly multi-part) send.
			select {
			case <-time.After(paceCap + time.Second):
			case <-ctx.Done():
			}
			if len(c.queue) == 0 {
				return
			}
		}
		select {
		case <-time.After(100 * time.Millisecond):
		case <-ctx.Done():
			return
		}
	}
}

type apiResponse struct {
	Status  string `json:"status"`
	Retcode int    `json:"retcode"`
	Message string `json:"message"`
}

func (c *Client) call(path string, payload any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	ctx := c.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("POST %s: %w", path, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("POST %s: status %d: %s", path, resp.StatusCode, body)
	}
	var ar apiResponse
	if err := json.Unmarshal(body, &ar); err != nil {
		return fmt.Errorf("POST %s: bad response %q", path, body)
	}
	if ar.Status == "failed" || ar.Retcode != 0 {
		return fmt.Errorf("POST %s: onebot retcode %d: %s", path, ar.Retcode, ar.Message)
	}
	return nil
}
