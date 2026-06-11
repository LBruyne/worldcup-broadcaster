package espn

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const userAgent = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0 Safari/537.36"

type Client struct {
	baseURL string // overridable for tests
	league  string
	http    *http.Client
}

func NewClient(league string) *Client {
	return &Client{
		baseURL: "https://site.api.espn.com/apis/site/v2/sports/soccer",
		league:  league,
		http:    &http.Client{Timeout: 10 * time.Second},
	}
}

// NewClientWithBase is used by tests to point at a fake server.
func NewClientWithBase(baseURL, league string) *Client {
	c := NewClient(league)
	c.baseURL = baseURL
	return c
}

// get fetches url with up to 3 attempts and exponential backoff.
func (c *Client) get(ctx context.Context, url string) ([]byte, error) {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-time.After(time.Duration(attempt) * time.Second):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", userAgent)
		resp, err := c.http.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			lastErr = err
			continue
		}
		if resp.StatusCode != http.StatusOK {
			lastErr = fmt.Errorf("GET %s: status %d", url, resp.StatusCode)
			continue
		}
		return body, nil
	}
	return nil, fmt.Errorf("after 3 attempts: %w", lastErr)
}

// Scoreboard fetches all events for a date or date range
// (YYYYMMDD or YYYYMMDD-YYYYMMDD).
func (c *Client) Scoreboard(ctx context.Context, dates string) (*Scoreboard, error) {
	url := fmt.Sprintf("%s/%s/scoreboard?dates=%s&limit=200", c.baseURL, c.league, dates)
	body, err := c.get(ctx, url)
	if err != nil {
		return nil, err
	}
	var sb Scoreboard
	if err := json.Unmarshal(body, &sb); err != nil {
		return nil, fmt.Errorf("parse scoreboard: %w", err)
	}
	return &sb, nil
}

// Summary fetches match detail. The raw body is returned alongside the
// parsed struct for persistence and LLM prompts.
func (c *Client) Summary(ctx context.Context, eventID string) (*Summary, []byte, error) {
	url := fmt.Sprintf("%s/%s/summary?event=%s", c.baseURL, c.league, eventID)
	body, err := c.get(ctx, url)
	if err != nil {
		return nil, nil, err
	}
	var s Summary
	if err := json.Unmarshal(body, &s); err != nil {
		return nil, nil, fmt.Errorf("parse summary %s: %w", eventID, err)
	}
	return &s, body, nil
}
