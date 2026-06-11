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
	"time"
)

type Client struct {
	baseURL  string
	token    string
	groupID  int64
	interval time.Duration
	http     *http.Client
	logger   *slog.Logger

	queue chan string
	// OnSendError, when set, is invoked for every failed group send so the
	// alerter can notify the admin.
	OnSendError func(error)
}

func New(baseURL, token string, groupID int64, interval time.Duration, logger *slog.Logger) *Client {
	return &Client{
		baseURL:  baseURL,
		token:    token,
		groupID:  groupID,
		interval: interval,
		http:     &http.Client{Timeout: 15 * time.Second},
		logger:   logger,
		queue:    make(chan string, 256),
	}
}

// Start launches the queue consumer; it drains until ctx is cancelled.
func (c *Client) Start(ctx context.Context) {
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case msg := <-c.queue:
				if err := c.SendGroupNow(msg); err != nil {
					c.logger.Error("send group message failed", "error", err)
					if c.OnSendError != nil {
						c.OnSendError(err)
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

// EnqueueGroup queues a group message; drops (with a log) when the queue is
// saturated rather than blocking match watchers.
func (c *Client) EnqueueGroup(msg string) {
	select {
	case c.queue <- msg:
	default:
		c.logger.Error("onebot queue full, dropping message", "message", msg)
	}
}

// SendGroupNow posts the message immediately, bypassing the queue.
// In log-only mode (group_id == 0) the message lands in the log instead.
func (c *Client) SendGroupNow(msg string) error {
	if c.groupID == 0 {
		c.logger.Warn("group_id not configured, message logged only", "message", msg)
		return nil
	}
	return c.call("/send_group_msg", map[string]any{
		"group_id": c.groupID,
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
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, c.baseURL+path, bytes.NewReader(raw))
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
