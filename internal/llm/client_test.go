package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestGenerate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer sk-test" {
			t.Errorf("auth = %s", r.Header.Get("Authorization"))
		}
		raw, _ := io.ReadAll(r.Body)
		var req map[string]any
		json.Unmarshal(raw, &req)
		if req["model"] != "deepseek-v4-pro" {
			t.Errorf("model = %v", req["model"])
		}
		if req["reasoning_effort"] != "high" {
			t.Errorf("reasoning_effort = %v", req["reasoning_effort"])
		}
		msgs := req["messages"].([]any)
		if len(msgs) != 2 || msgs[0].(map[string]any)["role"] != "system" {
			t.Errorf("messages = %v", msgs)
		}
		w.Write([]byte(`{"choices":[{"message":{"reasoning_content":"thinking...","content":"⭐ 看点：开幕战！"}}]}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "sk-test", "deepseek-v4-pro", 5*time.Second)
	got, err := c.Generate(context.Background(), "你是足球解说", "生成看点")
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if got != "⭐ 看点：开幕战！" {
		t.Errorf("content = %q", got)
	}
}

func TestGenerateErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusPaymentRequired)
		w.Write([]byte(`{"error":{"message":"Insufficient Balance"}}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "sk-test", "deepseek-v4-pro", 5*time.Second)
	if _, err := c.Generate(context.Background(), "s", "u"); err == nil {
		t.Fatal("expected error on non-200")
	}

	noKey := New(srv.URL, "", "deepseek-v4-pro", 5*time.Second)
	if _, err := noKey.Generate(context.Background(), "s", "u"); err == nil {
		t.Fatal("expected error when api key missing")
	}
}
