// Command ollama-demo 调一次 Ollama，打印回答与 token 用量。
//
//	go run ./cmd/ollama-demo
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/llm-gateway/llm-gateway/internal/config"
	"github.com/llm-gateway/llm-gateway/internal/llm"
)

func main() {
	cfg := config.Load()
	client, err := llm.New(cfg.OllamaBaseURL, cfg.OllamaModel)
	if err != nil {
		fmt.Fprintln(os.Stderr, "init:", err)
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	answer, usage, err := client.Generate(ctx, "用一句话解释什么是 RAG")
	if err != nil {
		fmt.Fprintln(os.Stderr, "generate:", err)
		os.Exit(1)
	}

	fmt.Printf("model: %s\n", cfg.OllamaModel)
	fmt.Printf("answer: %s\n", answer)
	fmt.Printf("tokens: %s\n", usage)
}
