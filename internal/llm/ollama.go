// Package llm 封装对 Ollama 的调用，统一返回 token 用量。
package llm

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/tmc/langchaingo/llms"
	"github.com/tmc/langchaingo/llms/ollama"

	"github.com/llm-gateway/llm-gateway/pkg/token"
)

// Client 是 Ollama 的薄封装。
type Client struct {
	llm   *ollama.LLM
	model string
	base  string
	http  *http.Client
}

// New 构造客户端。
func New(baseURL, model string) (*Client, error) {
	l, err := ollama.New(ollama.WithServerURL(baseURL), ollama.WithModel(model))
	if err != nil {
		return nil, fmt.Errorf("llm: init ollama: %w", err)
	}
	return &Client{
		llm:   l,
		model: model,
		base:  baseURL,
		http:  &http.Client{Timeout: 3 * time.Second},
	}, nil
}

// Model 返回当前模型名。
func (c *Client) Model() string { return c.model }

// Generate 生成一次回答，并返回 token 用量。
func (c *Client) Generate(ctx context.Context, prompt string) (string, token.Stats, error) {
	resp, err := c.llm.GenerateContent(ctx, []llms.MessageContent{
		llms.TextParts(llms.ChatMessageTypeHuman, prompt),
	}, llms.WithMaxTokens(512))
	if err != nil {
		return "", token.Stats{}, fmt.Errorf("llm: generate: %w", err)
	}
	if len(resp.Choices) == 0 {
		return "", token.Stats{}, fmt.Errorf("llm: empty response")
	}
	choice := resp.Choices[0]
	return choice.Content, token.FromGenerationInfo(choice.GenerationInfo), nil
}

// GenerateStream 流式生成：每拿到一段就回调 onDelta，最后返回完整文本与用量。
func (c *Client) GenerateStream(ctx context.Context, prompt string, onDelta func(string) error) (string, token.Stats, error) {
	var sb strings.Builder

	resp, err := c.llm.GenerateContent(ctx, []llms.MessageContent{
		llms.TextParts(llms.ChatMessageTypeHuman, prompt),
	}, llms.WithMaxTokens(512), llms.WithStreamingFunc(func(_ context.Context, chunk []byte) error {
		delta := string(chunk)
		sb.WriteString(delta)
		return onDelta(delta)
	}))
	if err != nil {
		return "", token.Stats{}, fmt.Errorf("llm: stream: %w", err)
	}
	if len(resp.Choices) == 0 {
		return "", token.Stats{}, fmt.Errorf("llm: empty response")
	}
	return sb.String(), token.FromGenerationInfo(resp.Choices[0].GenerationInfo), nil
}

// Healthy 探活：确认 Ollama 进程在跑且能响应。
func (c *Client) Healthy(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/api/tags", nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("ollama: status %d", resp.StatusCode)
	}
	return nil
}
