// Package llm calls the DeepSeek chat completions API (OpenAI wire format)
// to generate match previews and recaps. Callers must treat errors as
// non-fatal and degrade gracefully.
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

type Client struct {
	baseURL string
	apiKey  string
	model   string
	http    *http.Client
}

func New(baseURL, apiKey, model string, timeout time.Duration) *Client {
	return &Client{
		baseURL: baseURL,
		apiKey:  apiKey,
		model:   model,
		http:    &http.Client{Timeout: timeout},
	}
}

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type request struct {
	Model           string    `json:"model"`
	Messages        []message `json:"messages"`
	ReasoningEffort string    `json:"reasoning_effort"`
}

type response struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// Generate runs one chat completion and returns the assistant text.
func (c *Client) Generate(ctx context.Context, system, user string) (string, error) {
	if c.apiKey == "" {
		return "", errors.New("llm api key not configured")
	}
	raw, err := json.Marshal(request{
		Model: c.model,
		Messages: []message{
			{Role: "system", Content: system},
			{Role: "user", Content: user},
		},
		ReasoningEffort: "high",
	})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("llm request: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("llm status %d: %.300s", resp.StatusCode, body)
	}
	var r response
	if err := json.Unmarshal(body, &r); err != nil {
		return "", fmt.Errorf("llm parse: %w", err)
	}
	if r.Error != nil {
		return "", fmt.Errorf("llm error: %s", r.Error.Message)
	}
	if len(r.Choices) == 0 || r.Choices[0].Message.Content == "" {
		return "", errors.New("llm returned empty content")
	}
	return r.Choices[0].Message.Content, nil
}
