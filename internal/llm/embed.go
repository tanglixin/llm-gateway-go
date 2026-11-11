package llm

import (
	"context"
	"fmt"

	"github.com/tmc/langchaingo/llms/ollama"
)

// Embedder 调 Ollama 的 embedding 模型（默认 nomic-embed-text，768 维）。
type Embedder struct {
	llm   *ollama.LLM
	model string
}

// NewEmbedder 构造 embedding 客户端。
func NewEmbedder(baseURL, model string) (*Embedder, error) {
	l, err := ollama.New(ollama.WithServerURL(baseURL), ollama.WithModel(model))
	if err != nil {
		return nil, fmt.Errorf("llm: init embedder: %w", err)
	}
	return &Embedder{llm: l, model: model}, nil
}

// Model 返回模型名。
func (e *Embedder) Model() string { return e.model }

// Embed 把一段文本转成向量。
func (e *Embedder) Embed(ctx context.Context, text string) ([]float32, error) {
	vecs, err := e.llm.CreateEmbedding(ctx, []string{text})
	if err != nil {
		return nil, fmt.Errorf("llm: embed: %w", err)
	}
	if len(vecs) == 0 || len(vecs[0]) == 0 {
		return nil, fmt.Errorf("llm: empty embedding")
	}
	return vecs[0], nil
}
