package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// AnthropicClient calls the Anthropic Messages API. It targets DeepSeek's
// Anthropic-compatible endpoint (https://api.deepseek.com/anthropic), which
// supports the server-side web_search tool and thinking mode — giving /ask
// real, model-driven web search instead of the brittle DuckDuckGo scraper.
//
// DeepSeek's Anthropic gateway does NOT support MCP tools, so domain data (the
// football RAG) is supplied inline in the prompt by callers; this client only
// enables the built-in web_search server tool.
type AnthropicClient struct {
	baseURL   string
	apiKey    string
	model     string
	webSearch bool
	maxTokens int
	http      *http.Client
}

// NewAnthropic builds a client. webSearch enables the built-in web_search
// server tool on every call.
func NewAnthropic(baseURL, apiKey, model string, timeout time.Duration, webSearch bool) *AnthropicClient {
	return &AnthropicClient{
		baseURL:   strings.TrimRight(baseURL, "/"),
		apiKey:    apiKey,
		model:     model,
		webSearch: webSearch,
		maxTokens: 4096,
		http:      &http.Client{Timeout: timeout},
	}
}

// NativeSearch reports whether this client performs its own web search, so the
// QA layer can skip the legacy DuckDuckGo grounding path.
func (c *AnthropicClient) NativeSearch() bool { return c.webSearch }

const anthropicVersion = "2023-06-01"

type anthropicMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type anthropicThinking struct {
	Type         string `json:"type"`                    // "enabled"
	BudgetTokens int    `json:"budget_tokens,omitempty"` // ignored by DeepSeek, set for Anthropic validity
}

type anthropicTool struct {
	Type    string `json:"type"`
	Name    string `json:"name"`
	MaxUses int    `json:"max_uses,omitempty"`
}

type anthropicRequest struct {
	Model     string             `json:"model"`
	MaxTokens int                `json:"max_tokens"`
	System    string             `json:"system,omitempty"`
	Messages  []anthropicMessage `json:"messages"`
	Thinking  *anthropicThinking `json:"thinking,omitempty"`
	Tools     []anthropicTool    `json:"tools,omitempty"`
}

type anthropicContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type anthropicResponse struct {
	Content []anthropicContentBlock `json:"content"`
	Type    string                  `json:"type"`
	Error   *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

// Generate runs one message with thinking enabled, no web search — for digest
// commentary and other utility text generation.
func (c *AnthropicClient) Generate(ctx context.Context, system, user string) (string, error) {
	return c.generate(ctx, system, user, true, false)
}

// GenerateThink runs one Messages call WITHOUT web search. Used for structured
// / utility calls (routing, player-name translation) whose output must stay
// clean JSON/text — attaching web_search makes the model emit tool-call markup.
func (c *AnthropicClient) GenerateThink(ctx context.Context, system, user string, think bool) (string, error) {
	return c.generate(ctx, system, user, think, false)
}

// GenerateSearch runs one Messages call WITH the web_search server tool (when
// the client was built with web search enabled) — for the conversational /ask
// and @-engage answers that fact-check online.
func (c *AnthropicClient) GenerateSearch(ctx context.Context, system, user string, think bool) (string, error) {
	return c.generate(ctx, system, user, think, c.webSearch)
}

func (c *AnthropicClient) generate(ctx context.Context, system, user string, think, search bool) (string, error) {
	if c.apiKey == "" {
		return "", errors.New("llm api key not configured")
	}
	reqBody := anthropicRequest{
		Model:     c.model,
		MaxTokens: c.maxTokens,
		System:    system,
		Messages:  []anthropicMessage{{Role: "user", Content: user}},
	}
	if think {
		// budget_tokens must be < max_tokens for Anthropic; DeepSeek ignores it.
		reqBody.Thinking = &anthropicThinking{Type: "enabled", BudgetTokens: 2048}
	}
	if search {
		reqBody.Tools = []anthropicTool{{Type: "web_search_20250305", Name: "web_search", MaxUses: 5}}
	}
	raw, err := json.Marshal(reqBody)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/messages", bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", c.apiKey)
	req.Header.Set("anthropic-version", anthropicVersion)

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("llm request: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	var r anthropicResponse
	if jsonErr := json.Unmarshal(body, &r); jsonErr != nil {
		// non-JSON body (e.g. a gateway error page)
		return "", fmt.Errorf("llm status %d: %.300s", resp.StatusCode, body)
	}
	if r.Error != nil {
		return "", fmt.Errorf("llm error: %s", r.Error.Message)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("llm status %d: %.300s", resp.StatusCode, body)
	}
	var b strings.Builder
	for _, blk := range r.Content {
		if blk.Type == "text" {
			b.WriteString(blk.Text)
		}
	}
	out := b.String()
	if out == "" {
		return "", errors.New("llm returned no text content")
	}
	return out, nil
}
