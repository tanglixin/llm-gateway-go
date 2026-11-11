// Package token 提供 LLM 调用的 token 用量统计。
package token

import "fmt"

// Stats 是一次调用的 token 用量。
type Stats struct {
	Prompt     int `json:"prompt"`
	Completion int `json:"completion"`
}

// Total 返回总 token 数。
func (s Stats) Total() int { return s.Prompt + s.Completion }

// String 让日志里一眼能看懂。
func (s Stats) String() string {
	return fmt.Sprintf("prompt=%d completion=%d total=%d", s.Prompt, s.Completion, s.Total())
}

// FromGenerationInfo 从 langchaingo 的 GenerationInfo 提取用量。
//
// langchaingo 的 ollama 适配器写入的键是 PromptTokens / CompletionTokens / TotalTokens，
// 这里对常见别名做兼容，避免换 provider 后拿不到数。
func FromGenerationInfo(info map[string]any) Stats {
	if info == nil {
		return Stats{}
	}
	return Stats{
		Prompt:     firstInt(info, "PromptTokens", "PromptEvalCount", "prompt_tokens"),
		Completion: firstInt(info, "CompletionTokens", "EvalCount", "completion_tokens"),
	}
}

func firstInt(info map[string]any, keys ...string) int {
	for _, k := range keys {
		if v, ok := info[k]; ok {
			return asInt(v)
		}
	}
	return 0
}

func asInt(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case int32:
		return int(n)
	case int64:
		return int(n)
	case float64:
		return int(n)
	case float32:
		return int(n)
	}
	return 0
}

// Estimate 粗略估算一段文本的 token 数（ASCII 4 字符/token，CJK 1.5 字符/token）。
func Estimate(text string) int {
	var ascii, wide int
	for _, r := range text {
		if r < 128 {
			ascii++
		} else {
			wide++
		}
	}
	n := ascii/4 + wide*2/3
	if n < 1 {
		return 1
	}
	return n
}
