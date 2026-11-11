// Package agent 实现一个最小可用的 ReAct 循环：思考 → 调工具 → 观察 → 再思考。
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/llm-gateway/llm-gateway/internal/gateway"
)

// Step 是轨迹里的一步。
type Step struct {
	Index      int    `json:"index"`
	Kind       string `json:"kind"` // thought | action | observation | final
	Content    string `json:"content,omitempty"`
	Tool       string `json:"tool,omitempty"`
	Input      string `json:"input,omitempty"`
	DurationMs int64  `json:"durationMs,omitempty"`
	OK         bool   `json:"ok"`
}

// Result 是一次 Agent 运行的完整结果。
type Result struct {
	Steps      []Step   `json:"steps"`
	Grounded   bool     `json:"grounded"`   // 终答里的数字是否都能在观察里找到
	Unverified []string `json:"unverified"` // 找不到出处的数字
}

// Agent 跑 ReAct 循环。
type Agent struct {
	gw       *gateway.Gateway
	tools    []*Tool
	byName   map[string]*Tool
	maxSteps int
}

// New 构造 Agent。
func New(gw *gateway.Gateway, tools []*Tool, maxSteps int) *Agent {
	if maxSteps <= 0 {
		maxSteps = 3
	}
	m := make(map[string]*Tool, len(tools))
	for _, t := range tools {
		m[t.Name] = t
	}
	return &Agent{gw: gw, tools: tools, byName: m, maxSteps: maxSteps}
}

// Tools 返回工具列表（供前端展示）。
func (a *Agent) Tools() []*Tool { return a.tools }

func (a *Agent) systemPrompt() string {
	var sb strings.Builder
	sb.WriteString("你可以调用下面的工具：\n")
	for _, t := range a.tools {
		fmt.Fprintf(&sb, "- %s：%s，参数 %s\n", t.Name, t.Desc, t.Spec)
	}
	sb.WriteString(`
每一轮只输出一行，格式二选一：
TOOL: 工具名 {"参数":"值"}
FINAL: 最终回答

需要查资料就用 TOOL，拿到结果后再决定继续查还是给 FINAL。
`)
	return sb.String()
}

// Run 执行一次任务，返回完整轨迹。
//
// 结构是「约束解码的 ReAct」：小模型（qwen2:0.5b）写自由文本会跑偏，
// 所以每一步都只让它输出极短的结果——选工具时只输出工具名，
// 给参数时只输出 JSON。实测这样才稳定。
func (a *Agent) Run(ctx context.Context, task string) (Result, error) {
	steps := make([]Step, 0, 8)
	var observations []string
	used := map[string]bool{}
	history := task

	// 先检索知识库：内容类问题占多数，而且有了证据再让模型决定要不要补查，
	// 比让它从零挑工具可靠得多。
	if kb, ok := a.byName["kb_search"]; ok {
		t0 := time.Now()
		obs, runErr := kb.Run(ctx, map[string]any{"query": task, "top_k": 3.0})
		if runErr != nil {
			obs = "知识库检索失败：" + runErr.Error()
		}
		steps = append(steps, Step{Index: 0, Kind: "action", Tool: "kb_search",
			Input: fmt.Sprintf(`{"query":%q,"top_k":3}`, task), DurationMs: time.Since(t0).Milliseconds(), OK: runErr == nil})
		steps = append(steps, Step{Index: 1, Kind: "observation", Content: obs,
			DurationMs: time.Since(t0).Milliseconds(), OK: runErr == nil})
		observations = append(observations, obs)
		used["kb_search"] = true
		history += "\n已检索知识库，结果：" + truncate(obs, 200)
	}

	for i := 0; i < a.maxSteps; i++ {
		// 1) 选工具
		planPrompt := a.planPrompt(history)
		t0 := time.Now()
		pick, _, _, err := a.gw.GenerateWith(ctx, planPrompt, gateway.GenOpts{MaxTokens: 12, Temperature: 0})
		if err != nil {
			return Result{Steps: steps}, err
		}
		planMs := time.Since(t0).Milliseconds()
		out := strings.TrimSpace(pick)

		name := matchTool(out)
		source := "模型"
		if name == "" || !plausiblePick(name, task) {
			// 两种情况都落到规则：
			// 1) 模型没给出可用工具名
			// 2) 模型给了名字但与任务明显无关（实测它几乎永远回 usage_query）
			name = routeByRules(task)
			source = "规则兜底"
		}

		steps = append(steps, Step{
			Index: len(steps), Kind: "thought",
			Content:    fmt.Sprintf("%s选择 → %s（模型原始输出：%s）", source, name, truncate(out, 40)),
			DurationMs: planMs, OK: true,
		})

		if used[name] {
			steps = append(steps, Step{
				Index: len(steps), Kind: "thought",
				Content: fmt.Sprintf("%s 已经调用过，不再重复", name), OK: true,
			})
			break
		}

		tool := a.byName[name]
		used[name] = true

		// 2) 给参数
		args, rawArgs := a.genArgs(ctx, tool, task)
		steps = append(steps, Step{
			Index: len(steps), Kind: "action",
			Tool: name, Input: rawArgs, OK: true,
		})

		// 3) 执行
		t1 := time.Now()
		obs, runErr := tool.Run(ctx, args)
		dur := time.Since(t1).Milliseconds()
		if runErr != nil {
			obs = "工具报错：" + runErr.Error()
		}
		steps = append(steps, Step{
			Index: len(steps), Kind: "observation",
			Content: obs, DurationMs: dur, OK: runErr == nil,
		})
		observations = append(observations, obs)
		history += "\n已查询：" + name + "\n结果：" + truncate(obs, 200)
	}

	// 4) 根据观察给终答
	t2 := time.Now()
	final := "（没有获得可用信息）"
	if len(observations) > 0 {
		prompt := "根据资料回答问题，不要编造资料里没有的内容。\n\n资料：\n" +
			strings.Join(observations, "\n---\n") + "\n\n问题：" + task + "\n回答："
		if out, _, _, err := a.gw.GenerateWith(ctx, prompt, gateway.GenOpts{MaxTokens: 256, Temperature: 0}); err == nil && strings.TrimSpace(out) != "" {
			final = strings.TrimSpace(out)
		}
	}
	steps = append(steps, Step{
		Index: len(steps), Kind: "final", Content: final,
		DurationMs: time.Since(t2).Milliseconds(), OK: true,
	})

	grounded, unverified := verifyGrounding(final, observations)
	return Result{Steps: steps, Grounded: grounded, Unverified: unverified}, nil
}

// verifyGrounding 检查终答里的数字是否都能在观察里找到。
//
// 小模型很容易编数字（实测把 600 元说成 200 元），
// 这里不阻止它说，而是把「无法溯源」明确标出来。
func verifyGrounding(final string, observations []string) (bool, []string) {
	pool := strings.Join(observations, "\n")
	seen := map[string]bool{}
	var unverified []string

	for _, n := range numberPattern.FindAllString(final, -1) {
		if len(n) == 1 && n < "10" {
			continue // 单个数字多是列表序号
		}
		if seen[n] {
			continue
		}
		seen[n] = true
		if !strings.Contains(pool, n) {
			unverified = append(unverified, n)
		}
	}
	return len(unverified) == 0, unverified
}

var numberPattern = regexp.MustCompile(`[0-9]+(?:\.[0-9]+)?`)

// planPrompt 让模型只输出工具名。
func (a *Agent) planPrompt(history string) string {
	var sb strings.Builder
	sb.WriteString("可用工具：\n")
	for _, t := range a.tools {
		fmt.Fprintf(&sb, "%s：%s\n", t.Name, t.Desc)
	}
	sb.WriteString("\n问题：" + history)
	sb.WriteString("\n只输出一个工具名：")
	return sb.String()
}

// genArgs 让模型生成参数 JSON；失败时用启发式兜底。
func (a *Agent) genArgs(ctx context.Context, tool *Tool, task string) (map[string]any, string) {
	prompt := fmt.Sprintf("工具 %s 的参数格式：%s\n问题：%s\n只输出 JSON：", tool.Name, tool.Spec, task)
	raw, _, _, err := a.gw.GenerateWith(ctx, prompt, gateway.GenOpts{MaxTokens: 48, Temperature: 0})

	args := map[string]any{}
	text := strings.TrimSpace(raw)
	if i := strings.Index(text, "{"); i >= 0 {
		text = text[i:]
		if j := strings.LastIndex(text, "}"); j >= 0 {
			text = text[:j+1]
		}
		if json.Unmarshal([]byte(text), &args) == nil && len(args) > 0 {
			return args, text
		}
	}
	if err != nil {
		// 落回启发式
	}

	// 兜底：检索类工具直接用问题当查询词
	if tool.Name == "kb_search" {
		args["query"] = task
		args["top_k"] = 3.0
		return args, fmt.Sprintf(`{"query":%q,"top_k":3}（启发式兜底）`, task)
	}
	if tool.Name == "calculator" {
		args["expression"] = task
		return args, fmt.Sprintf(`{"expression":%q}（启发式兜底）`, task)
	}
	return args, "{}（启发式兜底）"
}

// plausiblePick 判断模型选的工具和任务是否沾边。
//
// 不是为了「聪明」，而是为了拦住明显的错配：
// 实测 qwen2:0.5b 不管问什么都回 usage_query，直接采信会把算数题也送去查账单。
func plausiblePick(name, task string) bool {
	t := strings.ToLower(task)
	has := func(words ...string) bool {
		for _, w := range words {
			if strings.Contains(t, w) {
				return true
			}
		}
		return false
	}

	switch name {
	case "usage_query":
		return has("成本", "花费", "花了", "费用", "用量", "调用", "多少钱", "token", "qps", "延迟")
	case "documents_list":
		return has("文档", "列出", "有哪些")
	case "calculator":
		return isArithmetic(task)
	case "time_now":
		return has("几点", "时间", "今天几号", "日期")
	case "kb_search":
		return true
	default:
		return false
	}
}

// routeByRules 是模型选不出工具时的确定性兜底。
//
// 小模型（qwen2:0.5b）在工具选择上几乎不可用：给它 3 个选项偶尔能答对，
// 给 5 个就开始输出「1、2、3、4、」这种。与其让它瞎选，不如用关键词兜底，
// 并在轨迹里标明是「规则兜底」，不假装是模型决定的。
func routeByRules(task string) string {
	t := strings.ToLower(task)

	hasAny := func(words ...string) bool {
		for _, w := range words {
			if strings.Contains(t, w) {
				return true
			}
		}
		return false
	}

	switch {
	case hasAny("成本", "花费", "花了", "费用", "用量", "调用", "多少钱", "token"):
		return "usage_query"
	case hasAny("哪些文档", "列出文档", "文档列表", "知识库里有什么"):
		return "documents_list"
	case hasAny("几点", "现在时间", "今天几号"):
		return "time_now"
	case isArithmetic(task):
		return "calculator"
	default:
		return "kb_search"
	}
}

// isArithmetic 判断任务是不是纯算术（只含数字和运算符）。
func isArithmetic(task string) bool {
	t := strings.ReplaceAll(task, "等于多少", "")
	t = strings.ReplaceAll(t, "等于", "")
	t = strings.ReplaceAll(t, "怎么算", "")
	t = strings.ReplaceAll(t, "算一下", "")
	t = strings.ReplaceAll(t, "？", "")
	t = strings.TrimSpace(t)

	hasDigit, hasOp := false, false
	for _, r := range t {
		switch {
		case r >= '0' && r <= '9' || r == '.':
			hasDigit = true
		case r == '+' || r == '-' || r == '*' || r == '/' || r == '(' || r == ')':
			hasOp = true
		case r == ' ':
		default:
			return false
		}
	}
	return hasDigit && hasOp
}

// matchTool 从模型输出里认出工具名；认不出返回空。
func matchTool(out string) string {
	lower := strings.ToLower(out)
	for _, name := range []string{"kb_search", "usage_query", "documents_list", "calculator", "time_now"} {
		if strings.Contains(lower, name) {
			return name
		}
	}
	return ""
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func (a *Agent) toolNames() string {
	out := make([]string, 0, len(a.tools))
	for _, t := range a.tools {
		out = append(out, t.Name)
	}
	return strings.Join(out, ", ")
}

type action struct {
	Tool    string
	Args    map[string]any
	RawArgs string
}

var toolNames = map[string]bool{
	"kb_search": true, "usage_query": true, "documents_list": true,
	"calculator": true, "time_now": true,
}

// parseAction 从模型输出里解析工具调用。
//
// 小模型的格式稳定性很差，所以这里做宽松解析：
// "TOOL: 名字 {json}" / "action: 名字 {json}" / 纯 JSON 都接受。
func parseAction(out string) (action, bool) {
	text := strings.TrimSpace(out)

	// 纯 JSON
	if strings.HasPrefix(text, "{") {
		var m map[string]any
		if err := json.Unmarshal([]byte(text), &m); err == nil {
			name := str(m["tool"])
			if name == "" {
				name = str(m["action"])
			}
			if toolNames[name] {
				args := map[string]any{}
				if a, ok := m["args"].(map[string]any); ok {
					args = a
				} else if a, ok := m["arguments"].(map[string]any); ok {
					args = a
				}
				return action{Tool: name, Args: args, RawArgs: text}, true
			}
		}
	}

	lower := strings.ToLower(text)
	idx := strings.Index(lower, "tool:")
	if idx < 0 {
		idx = strings.Index(lower, "action:")
	}
	if idx < 0 {
		return action{}, false
	}

	rest := strings.TrimSpace(text[idx+len("tool:"):])
	// 只取第一行，避免模型在后面又写解释
	if nl := strings.IndexByte(rest, '\n'); nl >= 0 {
		rest = rest[:nl]
	}

	fields := strings.Fields(rest)
	if len(fields) == 0 {
		return action{}, false
	}
	name := strings.Trim(fields[0], "：:，,")
	if !toolNames[name] {
		return action{}, false
	}

	args := map[string]any{}
	raw := ""
	if brace := strings.Index(rest, "{"); brace >= 0 {
		raw = rest[brace:]
		if err := json.Unmarshal([]byte(raw), &args); err != nil {
			// JSON 坏了就退回空参数，工具自己会报错
			args = map[string]any{}
		}
	}
	return action{Tool: name, Args: args, RawArgs: raw}, true
}
