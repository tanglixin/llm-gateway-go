// Package gateway 是多 provider 路由层：加权选择、熔断、故障转移。
package gateway

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/tmc/langchaingo/llms"
	"github.com/tmc/langchaingo/llms/ollama"
	"github.com/tmc/langchaingo/llms/openai"

	"github.com/llm-gateway/llm-gateway/pkg/token"
)

// ErrNoProvider 表示所有 provider 都不可用。
var ErrNoProvider = errors.New("gateway: 所有 provider 均不可用")

// 熔断参数：连续失败 3 次断开，30 秒后半开试探。
const (
	failThreshold = 3
	openCooldown  = 30 * time.Second
)

// CircuitState 是熔断器状态。
type CircuitState string

const (
	StateClosed CircuitState = "closed" // 正常
	StateOpen   CircuitState = "open"   // 断开，不再选它
	StateHalf   CircuitState = "half"   // 冷却结束，允许试探一次
)

// Provider 是一个上游模型提供方。
type Provider struct {
	Name   string `json:"name"`
	Kind   string `json:"kind"` // ollama | openai
	Model  string `json:"model"`
	Weight int    `json:"weight"`

	mu        sync.Mutex
	current   int       // 平滑加权轮询的当前权重
	fails     int       // 连续失败
	openedAt  time.Time // 熔断开启时间
	requests  int64
	errors    int64
	lastError string

	generate func(ctx context.Context, prompt string, onDelta func(string) error) (string, token.Stats, error)
	probe    func(ctx context.Context) error
}

// Status 是给前端看的 provider 状态。
type Status struct {
	Name      string       `json:"name"`
	Kind      string       `json:"kind"`
	Model     string       `json:"model"`
	Weight    int          `json:"weight"`
	State     CircuitState `json:"state"`
	Requests  int64        `json:"requests"`
	Errors    int64        `json:"errors"`
	LastError string       `json:"lastError,omitempty"`
}

func (p *Provider) state() CircuitState {
	if p.openedAt.IsZero() {
		return StateClosed
	}
	if time.Since(p.openedAt) >= openCooldown {
		return StateHalf
	}
	return StateOpen
}

func (p *Provider) available() bool {
	return p.state() != StateOpen
}

func (p *Provider) onSuccess() {
	p.mu.Lock()
	p.fails = 0
	p.openedAt = time.Time{}
	p.requests++
	p.mu.Unlock()
}

func (p *Provider) onFailure(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.fails++
	p.errors++
	p.requests++
	p.lastError = err.Error()
	if p.fails >= failThreshold {
		p.openedAt = time.Now()
	}
}

// Gateway 负责在多个 provider 之间路由。
type Gateway struct {
	providers []*Provider

	mu    sync.Mutex
	drill string // 演练中强制断开的 provider
}

// New 用给定 provider 构造网关。
func New(providers []*Provider) *Gateway {
	return &Gateway{providers: providers}
}

// Providers 返回全部 provider（只读）。
func (g *Gateway) Providers() []*Provider { return g.providers }

// PrimaryModel 返回第一个 provider 的模型名（用于会话记录）。
func (g *Gateway) PrimaryModel() string {
	if len(g.providers) == 0 {
		return ""
	}
	return g.providers[0].Model
}

// Names 返回全部 provider 名称。
func (g *Gateway) Names() []string {
	out := make([]string, len(g.providers))
	for i, p := range g.providers {
		out[i] = p.Name
	}
	return out
}

// next 用标准平滑加权轮询（nginx 算法）选下一个 provider：
// 每个 current += weight，选最大的，再减去总权重。
// 效果是 80/20 会变成 A A A A B 的均匀分布，而不是前 8 次全是 A。
func (g *Gateway) next() *Provider {
	g.mu.Lock()
	defer g.mu.Unlock()

	var best *Provider
	total := 0
	for _, p := range g.providers {
		if !p.available() {
			continue
		}
		total += p.Weight
		p.current += p.Weight
		if best == nil || p.current > best.current {
			best = p
		}
	}
	if best == nil {
		return nil
	}
	best.current -= total
	return best
}

// Generate 非流式生成，失败自动换下一个 provider。
func (g *Gateway) Generate(ctx context.Context, prompt string) (string, token.Stats, string, error) {
	return g.run(ctx, prompt, nil)
}

// GenerateStream 流式生成，同样支持故障转移。
// 注意：开始推流后再失败就没法换 provider 了（已经发出去了），只能在选之前换。
func (g *Gateway) GenerateStream(ctx context.Context, prompt string, onDelta func(string) error) (string, token.Stats, string, error) {
	return g.run(ctx, prompt, onDelta)
}

func (g *Gateway) run(ctx context.Context, prompt string, onDelta func(string) error) (string, token.Stats, string, error) {
	tried := make(map[*Provider]bool, len(g.providers))

	for len(tried) < len(g.providers) {
		p := g.next()
		if p == nil || tried[p] {
			break
		}
		tried[p] = true

		c, cancel := context.WithTimeout(ctx, 3*time.Minute)
		answer, usage, err := p.generate(c, prompt, onDelta)
		cancel()

		if err != nil {
			p.onFailure(err)
			if onDelta != nil {
				// 已经开始推流，无法换车
				return "", usage, p.Name, err
			}
			continue
		}
		p.onSuccess()
		return answer, usage, p.Name, nil
	}
	return "", token.Stats{}, "", ErrNoProvider
}

// Status 返回所有 provider 的状态。
func (g *Gateway) Status() []Status {
	out := make([]Status, 0, len(g.providers))
	for _, p := range g.providers {
		p.mu.Lock()
		st := Status{
			Name:      p.Name,
			Kind:      p.Kind,
			Model:     p.Model,
			Weight:    p.Weight,
			State:     p.state(),
			Requests:  p.requests,
			Errors:    p.errors,
			LastError: p.lastError,
		}
		p.mu.Unlock()
		out = append(out, st)
	}
	return out
}

// Drill 开启/关闭故障演练：强制把某个 provider 标成熔断。
func (g *Gateway) Drill(enable bool, name string) {
	for _, p := range g.providers {
		if name != "" && p.Name != name {
			continue
		}
		p.mu.Lock()
		if enable {
			p.openedAt = time.Now()
			p.fails = failThreshold
		} else {
			p.openedAt = time.Time{}
			p.fails = 0
		}
		p.mu.Unlock()
	}
}

// Healthy 探活所有 provider，返回第一个可用的。
func (g *Gateway) Healthy(ctx context.Context) error {
	var firstErr error
	for _, p := range g.providers {
		if p.probe == nil {
			continue
		}
		if err := p.probe(ctx); err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("%s: %w", p.Name, err)
			}
			continue
		}
		return nil
	}
	if firstErr != nil {
		return firstErr
	}
	return ErrNoProvider
}

// NewOllamaProvider 构造一个 Ollama provider。
func NewOllamaProvider(name, baseURL, model string, weight int) (*Provider, error) {
	client, err := ollama.New(ollama.WithServerURL(baseURL), ollama.WithModel(model))
	if err != nil {
		return nil, fmt.Errorf("gateway: init ollama %s: %w", name, err)
	}
	p := &Provider{Name: name, Kind: "ollama", Model: model, Weight: weight}
	p.generate = func(ctx context.Context, prompt string, onDelta func(string) error) (string, token.Stats, error) {
		return generateWith(ctx, client, prompt, onDelta)
	}
	p.probe = func(ctx context.Context) error { return probeURL(ctx, baseURL+"/api/tags") }
	return p, nil
}

// NewOpenAIProvider 构造一个 OpenAI 兼容 provider。
func NewOpenAIProvider(name, baseURL, apiKey, model string, weight int) (*Provider, error) {
	opts := []openai.Option{openai.WithModel(model)}
	if baseURL != "" {
		opts = append(opts, openai.WithBaseURL(baseURL))
	}
	if apiKey != "" {
		opts = append(opts, openai.WithToken(apiKey))
	}
	client, err := openai.New(opts...)
	if err != nil {
		return nil, fmt.Errorf("gateway: init openai %s: %w", name, err)
	}
	p := &Provider{Name: name, Kind: "openai", Model: model, Weight: weight}
	p.generate = func(ctx context.Context, prompt string, onDelta func(string) error) (string, token.Stats, error) {
		return generateWith(ctx, client, prompt, onDelta)
	}
	p.probe = func(ctx context.Context) error { return probeURL(ctx, baseURL+"/models") }
	return p, nil
}

func generateWith(ctx context.Context, m llms.Model, prompt string, onDelta func(string) error) (string, token.Stats, error) {
	opts := []llms.CallOption{llms.WithMaxTokens(512)}
	var sb stringsBuilder
	if onDelta != nil {
		opts = append(opts, llms.WithStreamingFunc(func(_ context.Context, chunk []byte) error {
			sb.Write(chunk)
			return onDelta(string(chunk))
		}))
	}

	resp, err := m.GenerateContent(ctx, []llms.MessageContent{
		llms.TextParts(llms.ChatMessageTypeHuman, prompt),
	}, opts...)
	if err != nil {
		return "", token.Stats{}, err
	}
	if len(resp.Choices) == 0 {
		return "", token.Stats{}, fmt.Errorf("gateway: empty response")
	}
	choice := resp.Choices[0]
	text := choice.Content
	if onDelta != nil {
		text = sb.String()
	}
	return text, token.FromGenerationInfo(choice.GenerationInfo), nil
}
