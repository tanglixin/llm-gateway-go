package llm_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/llm-gateway/llm-gateway/internal/llm"
)

// fakeOllama 模拟 POST /api/chat，返回带 token 用量的响应。
func fakeOllama(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			http.NotFound(w, r)
			return
		}
		var req struct {
			Model    string `json:"model"`
			Messages []any  `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if req.Model != "qwen2:7b" {
			t.Errorf("model = %q", req.Model)
		}
		// Ollama 用 NDJSON 流：必须单行 + 换行结尾，多行 JSON 会被逐行解析而失败。
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = w.Write([]byte(`{"model":"qwen2:7b","message":{"role":"assistant","content":"RAG 是检索增强生成。"},"done":true,"prompt_eval_count":11,"eval_count":22}` + "\n"))
	}))
}

func TestGenerateReturnsUsage(t *testing.T) {
	srv := fakeOllama(t)
	defer srv.Close()

	client, err := llm.New(srv.URL, "qwen2:7b")
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	answer, usage, err := client.Generate(context.Background(), "什么是 RAG")
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if !strings.Contains(answer, "RAG") {
		t.Fatalf("answer = %q", answer)
	}
	if usage.Prompt != 11 || usage.Completion != 22 || usage.Total() != 33 {
		t.Fatalf("usage = %+v", usage)
	}
}

func TestGeneratePropagatesUpstreamError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "model not found", http.StatusNotFound)
	}))
	defer srv.Close()

	client, err := llm.New(srv.URL, "nope")
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if _, _, err := client.Generate(context.Background(), "hi"); err == nil {
		t.Fatal("expected error for 404 upstream")
	}
}

func TestHealthy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tags" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"models":[]}`))
	}))
	defer srv.Close()

	client, err := llm.New(srv.URL, "qwen2:7b")
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if err := client.Healthy(context.Background()); err != nil {
		t.Fatalf("healthy: %v", err)
	}
}
