package agent

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/llm-gateway/llm-gateway/internal/llm"
	"github.com/llm-gateway/llm-gateway/internal/store"
)

// Tool 是一个可被 Agent 调用的工具。
type Tool struct {
	Name string
	Desc string
	Spec string // 给模型看的参数说明
	Run  func(ctx context.Context, args map[string]any) (string, error)
}

// NewTools 构造工具集。每个工具都直接打到真实数据上。
func NewTools(db *store.Store, emb *llm.Embedder, tenant string) []*Tool {
	return []*Tool{
		{
			Name: "kb_search",
			Desc: "在知识库里做语义检索，返回最相关的文档片段",
			Spec: `{"query": "检索词", "top_k": 3}`,
			Run: func(ctx context.Context, args map[string]any) (string, error) {
				q := str(args["query"])
				if q == "" {
					return "", fmt.Errorf("query 不能为空")
				}
				k := 3
				if v, ok := args["top_k"].(float64); ok && v > 0 {
					k = int(v)
				}
				vec, err := emb.Embed(ctx, q)
				if err != nil {
					return "", err
				}
				hits, err := db.SearchChunks(ctx, tenant, vec, k)
				if err != nil {
					return "", err
				}
				if len(hits) == 0 {
					return "知识库没有命中任何片段", nil
				}
				var sb strings.Builder
				for i, h := range hits {
					fmt.Fprintf(&sb, "[%d] 《%s》相关度 %.2f\n%s\n", i+1, h.Name, h.Score, h.Content)
				}
				return sb.String(), nil
			},
		},
		{
			Name: "usage_query",
			Desc: "查询本租户的调用量与成本统计",
			Spec: `{}`,
			Run: func(ctx context.Context, args map[string]any) (string, error) {
				sum, err := db.Usage(ctx, tenant, time.Time{})
				if err != nil {
					return "", err
				}
				return fmt.Sprintf("调用 %d 次，输入 %d tokens，输出 %d tokens，估算成本 ¥%.4f，平均延迟 %d ms，P95 %d ms",
					sum.Requests, sum.TokensIn, sum.TokensOut, sum.CostCNY, sum.AvgLatency, sum.P95Latency), nil
			},
		},
		{
			Name: "documents_list",
			Desc: "列出知识库里的文档及其分块数",
			Spec: `{}`,
			Run: func(ctx context.Context, args map[string]any) (string, error) {
				docs, err := db.ListDocuments(ctx, tenant)
				if err != nil {
					return "", err
				}
				if len(docs) == 0 {
					return "知识库为空", nil
				}
				var sb strings.Builder
				for _, d := range docs {
					fmt.Fprintf(&sb, "- %s（%d 块，%s）\n", d.Name, d.Chunks, d.Status)
				}
				return sb.String(), nil
			},
		},
		{
			Name: "calculator",
			Desc: "计算一个算术表达式，支持 + - * / 和括号",
			Spec: `{"expression": "600*3+200"}`,
			Run: func(ctx context.Context, args map[string]any) (string, error) {
				expr := str(args["expression"])
				v, err := Eval(expr)
				if err != nil {
					return "", err
				}
				return strconv.FormatFloat(v, 'f', -1, 64), nil
			},
		},
		{
			Name: "time_now",
			Desc: "获取当前时间",
			Spec: `{}`,
			Run: func(ctx context.Context, args map[string]any) (string, error) {
				return time.Now().Format("2006-01-02 15:04:05 MST"), nil
			},
		},
	}
}

func str(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}
