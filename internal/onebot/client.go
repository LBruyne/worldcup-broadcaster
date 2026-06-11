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
	"net/http"
	"strings"
	"time"
)

type groupMsg struct {
	groupID int64
	text    string
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
		queue: make(chan groupMsg, 1024),
	}
}

// Start launches the queue consumer; it drains until ctx is cancelled.
func (c *Client) Start(ctx context.Context) {
	c.ctx = ctx
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case msg := <-c.queue:
				// Very long messages both trip NapCat's 30s send ack and
				// look like spam to QQ risk control: send them in chunks,
				// spaced like ordinary messages.
				for i, part := range splitMessage(msg.text, maxMsgRunes) {
					if i > 0 {
						select {
						case <-time.After(c.interval):
						case <-ctx.Done():
							return
						}
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
						c.logger.Error("send group message failed", "error", err)
						if c.OnSendError != nil {
							// Off the consumer goroutine: an alert (private msg
							// over the same HTTP endpoint) must not stall the
							// group-message queue.
							go c.OnSendError(err)
						}
					}
				}
				select {
				case <-time.After(c.interval):
				case <-ctx.Done():
					return
				}
			}
		}
	}()
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
			select {
			case <-time.After(c.interval + time.Second):
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
