// Package dingtalk posts messages to a DingTalk group robot webhook,
// supporting the optional HMAC-SHA256 signature security setting.
package dingtalk

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Client struct {
	webhook string
	secret  string
	keyword string // custom-keyword security: auto-prefixed when missing
	http    *http.Client
}

func New(webhook, secret string) *Client {
	return &Client{webhook: webhook, secret: secret, http: &http.Client{Timeout: 10 * time.Second}}
}

// SetKeyword configures the robot's custom-keyword security term; outgoing
// messages that don't already contain it get it prefixed.
func (c *Client) SetKeyword(kw string) { c.keyword = kw }

func (c *Client) withKeyword(s string) string {
	if c.keyword == "" || strings.Contains(s, c.keyword) {
		return s
	}
	return "【" + c.keyword + "】" + s
}

// signedURL appends timestamp+sign when a secret is configured.
func (c *Client) signedURL() string {
	if c.secret == "" {
		return c.webhook
	}
	ts := fmt.Sprint(time.Now().UnixMilli())
	mac := hmac.New(sha256.New, []byte(c.secret))
	mac.Write([]byte(ts + "\n" + c.secret))
	sign := url.QueryEscape(base64.StdEncoding.EncodeToString(mac.Sum(nil)))
	return c.webhook + "&timestamp=" + ts + "&sign=" + sign
}

// SendText posts a plain text message.
func (c *Client) SendText(text string) error {
	return c.post(map[string]any{
		"msgtype": "text",
		"text":    map[string]string{"content": c.withKeyword(text)},
	})
}

// SendMarkdown posts a markdown message (supports image embeds by URL).
func (c *Client) SendMarkdown(title, md string) error {
	return c.post(map[string]any{
		"msgtype":  "markdown",
		"markdown": map[string]string{"title": c.withKeyword(title), "text": c.withKeyword(md)},
	})
}

func (c *Client) post(payload any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	resp, err := c.http.Post(c.signedURL(), "application/json", bytes.NewReader(raw))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	var r struct {
		ErrCode int    `json:"errcode"`
		ErrMsg  string `json:"errmsg"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return fmt.Errorf("dingtalk response: %.200s", body)
	}
	if r.ErrCode != 0 {
		return fmt.Errorf("dingtalk errcode %d: %s", r.ErrCode, r.ErrMsg)
	}
	return nil
}
