package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestAnthropicGenerateWithSearch(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if r.Header.Get("x-api-key") != "sk-test" {
			t.Errorf("x-api-key = %s", r.Header.Get("x-api-key"))
		}
		if r.Header.Get("anthropic-version") == "" {
			t.Error("missing anthropic-version header")
		}
		raw, _ := io.ReadAll(r.Body)
		json.Unmarshal(raw, &gotBody)
		// final answer arrives as a text block alongside server-tool/thinking blocks
		w.Write([]byte(`{"content":[
			{"type":"thinking","thinking":"let me search"},
			{"type":"server_tool_use","id":"x","name":"web_search","input":{"query":"messi goals"}},
			{"type":"web_search_tool_result","tool_use_id":"x","content":[]},
			{"type":"text","text":"梅西本赛季打进 25 球 ⚽"}
		],"stop_reason":"end_turn"}`))
	}))
	defer srv.Close()

	c := NewAnthropic(srv.URL, "sk-test", "deepseek-v4-pro", 5*time.Second, true)
	if !c.NativeSearch() {
		t.Error("NativeSearch should be true when web search enabled")
	}
	got, err := c.GenerateThink(context.Background(), "你是足球助手", "梅西进了多少球", true)
	if err != nil {
		t.Fatalf("GenerateThink: %v", err)
	}
	if got != "梅西本赛季打进 25 球 ⚽" {
		t.Errorf("content = %q", got)
	}
	// web_search tool must be advertised
	tools, _ := gotBody["tools"].([]any)
	if len(tools) == 0 {
		t.Fatal("expected web_search tool in request")
	}
	if tools[0].(map[string]any)["name"] != "web_search" {
		t.Errorf("tool = %v", tools[0])
	}
	// thinking enabled for hard questions
	think, _ := gotBody["thinking"].(map[string]any)
	if think["type"] != "enabled" {
		t.Errorf("thinking = %v", gotBody["thinking"])
	}
	// system prompt + single user message
	if gotBody["system"] != "你是足球助手" {
		t.Errorf("system = %v", gotBody["system"])
	}
}

func TestAnthropicNoThinkNoSearch(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		json.Unmarshal(raw, &gotBody)
		w.Write([]byte(`{"content":[{"type":"text","text":"ok"}]}`))
	}))
	defer srv.Close()

	c := NewAnthropic(srv.URL, "sk-test", "deepseek-v4-flash", 5*time.Second, false)
	if c.NativeSearch() {
		t.Error("NativeSearch should be false when web search disabled")
	}
	got, err := c.GenerateThink(context.Background(), "s", "u", false)
	if err != nil {
		t.Fatalf("GenerateThink: %v", err)
	}
	if got != "ok" {
		t.Errorf("got %q", got)
	}
	if _, ok := gotBody["tools"]; ok {
		t.Error("no tools expected when web search disabled")
	}
	if _, ok := gotBody["thinking"]; ok {
		t.Error("no thinking field expected when think=false")
	}
}

func TestAnthropicErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"type":"error","error":{"type":"invalid_request_error","message":"bad model"}}`))
	}))
	defer srv.Close()

	c := NewAnthropic(srv.URL, "sk-test", "deepseek-v4-pro", 5*time.Second, true)
	_, err := c.Generate(context.Background(), "s", "u")
	if err == nil || !strings.Contains(err.Error(), "bad model") {
		t.Fatalf("expected error mentioning bad model, got %v", err)
	}

	noKey := NewAnthropic(srv.URL, "", "deepseek-v4-pro", 5*time.Second, true)
	if _, err := noKey.Generate(context.Background(), "s", "u"); err == nil {
		t.Fatal("expected error when api key missing")
	}
}

func TestAnthropicMultipleTextBlocks(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"content":[{"type":"text","text":"第一段"},{"type":"text","text":"第二段"}]}`))
	}))
	defer srv.Close()
	c := NewAnthropic(srv.URL, "sk-test", "deepseek-v4-pro", 5*time.Second, false)
	got, err := c.Generate(context.Background(), "s", "u")
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if got != "第一段第二段" {
		t.Errorf("got %q", got)
	}
}
