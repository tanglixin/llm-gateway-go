package httpapi

import (
	"context"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/llm-gateway/llm-gateway/internal/search"
	"github.com/llm-gateway/llm-gateway/internal/store"
)

// baselineRecall 是 CI 闸门的基线：低于它直接阻断。
const baselineRecall = 0.80

// activeVersion 返回当前生效的提示词版本；没有就退回默认模板。
func (s *Server) activeVersion(ctx context.Context) store.PromptVersion {
	if v, err := s.db.ActivePromptVersion(ctx); err == nil {
		return *v
	}
	return store.PromptVersion{
		Name:          "builtin",
		RetrievalMode: "hybrid",
		TopK:          3,
		Template:      "根据资料回答问题，不要编造资料里没有的内容。\n\n资料：\n{{context}}\n问题：{{question}}\n回答：",
	}
}

// evalVersions 返回全部提示词版本及最近一次评测结果。
func (s *Server) evalVersions(c *gin.Context) {
	versions, err := s.db.ListPromptVersions(c.Request.Context())
	if err != nil {
		s.fail(c, err)
		return
	}
	runs, err := s.db.LatestEvalRuns(c.Request.Context())
	if err != nil {
		s.fail(c, err)
		return
	}

	byVersion := make(map[string]store.EvalRun, len(runs))
	for _, r := range runs {
		byVersion[r.Version] = r
	}

	out := make([]gin.H, 0, len(versions))
	for _, v := range versions {
		item := gin.H{
			"name":          v.Name,
			"description":   v.Description,
			"retrievalMode": v.RetrievalMode,
			"topK":          v.TopK,
			"isActive":      v.IsActive,
		}
		if r, ok := byVersion[v.Name]; ok {
			item["run"] = r
		}
		out = append(out, item)
	}

	golden := make([]gin.H, 0, len(search.GoldenSet))
	for _, gc := range search.GoldenSet {
		golden = append(golden, gin.H{"query": gc.Query, "doc": gc.Doc})
	}

	c.JSON(http.StatusOK, gin.H{
		"versions":  out,
		"cases":     len(search.GoldenSet),
		"baseline":  baselineRecall,
		"goldenSet": golden,
	})
}

// evalRun 对指定版本（或全部）跑一次真实回归。
func (s *Server) evalRun(c *gin.Context) {
	var in struct {
		Version string `json:"version"`
	}
	_ = c.ShouldBindJSON(&in)

	ctx := c.Request.Context()
	versions, err := s.db.ListPromptVersions(ctx)
	if err != nil {
		s.fail(c, err)
		return
	}
	if in.Version != "" {
		filtered := versions[:0]
		for _, v := range versions {
			if v.Name == in.Version {
				filtered = append(filtered, v)
			}
		}
		versions = filtered
	}
	if len(versions) == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"code": "not_found", "message": "版本不存在"}})
		return
	}

	results := make([]store.EvalRun, 0, len(versions))
	for _, v := range versions {
		run, err := s.evalOneVersion(ctx, s.cfg.Tenant, v)
		if err != nil {
			s.fail(c, err)
			return
		}
		if err := s.db.InsertEvalRun(ctx, run); err != nil {
			s.fail(c, err)
			return
		}
		results = append(results, run)
	}

	c.JSON(http.StatusOK, gin.H{"results": results, "baseline": baselineRecall})
}

// evalActivate 切换生效版本（admin）。
func (s *Server) evalActivate(c *gin.Context) {
	var in struct {
		Version string `json:"version"`
	}
	if err := c.ShouldBindJSON(&in); err != nil || strings.TrimSpace(in.Version) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "invalid_request", "message": "version 必填"}})
		return
	}
	if err := s.db.ActivatePromptVersion(c.Request.Context(), in.Version); err != nil {
		s.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"active": in.Version})
}

// evalOneVersion 用该版本的检索配置跑 Golden Set，算真实召回指标。
func (s *Server) evalOneVersion(ctx context.Context, tenant string, v store.PromptVersion) (store.EvalRun, error) {
	start := time.Now()

	var recall, mrr, ndcg float64
	for _, gc := range search.GoldenSet {
		hits, err := s.searchAll(ctx, tenant, gc.Query, v.RetrievalMode, v.TopK, defaultAlpha)
		if err != nil {
			return store.EvalRun{}, err
		}

		rank := 0
		for i, h := range hits {
			if i >= v.TopK {
				break
			}
			if h.Name == gc.Doc {
				rank = i + 1
				break
			}
		}
		if rank > 0 {
			recall++
			mrr += 1 / float64(rank)
			ndcg += 1 / math.Log2(float64(rank)+1)
		}
	}

	n := float64(len(search.GoldenSet))
	run := store.EvalRun{
		Version: v.Name,
		Cases:   len(search.GoldenSet),
		Recall:  round3(recall / n),
		MRR:     round3(mrr / n),
		NDCG:    round3(ndcg / n),
		TookMs:  int(time.Since(start).Milliseconds()),
	}
	if run.Recall >= baselineRecall {
		run.Gate = "passed"
	} else {
		run.Gate = "blocked"
	}
	return run, nil
}

func round3(v float64) float64 { return math.Round(v*1000) / 1000 }

// SeedPromptVersions 写入内置版本；已存在则更新模板与配置。
func seedPromptVersions(ctx context.Context, db *store.Store) error {
	versions := []store.PromptVersion{
		{
			Name:          "prompt_v1",
			Description:   "基线 CoT，纯向量检索",
			RetrievalMode: "vector",
			TopK:          3,
			Template:      "请一步步思考后回答。\n\n资料：\n{{context}}\n问题：{{question}}\n回答：",
		},
		{
			Name:          "prompt_v2",
			Description:   "改为混合检索 + 结构化输出",
			RetrievalMode: "hybrid",
			TopK:          3,
			Template:      "根据资料回答问题，先给结论再给依据。\n\n资料：\n{{context}}\n问题：{{question}}\n回答：",
		},
		{
			Name:          "prompt_v3",
			Description:   "混合检索 + 强制引用编号（当前生效）",
			RetrievalMode: "hybrid",
			TopK:          3,
			Template:      "根据资料回答问题，不要编造资料里没有的内容，并在结论后标注引用编号。\n\n资料：\n{{context}}\n问题：{{question}}\n回答：",
		},
		{
			Name:          "prompt_v4",
			Description:   "实验版：只用 BM25 且只取 1 篇，省延迟",
			RetrievalMode: "bm25",
			TopK:          1,
			Template:      "根据资料回答问题。\n\n资料：\n{{context}}\n问题：{{question}}\n回答：",
		},
	}

	for _, v := range versions {
		if err := db.UpsertPromptVersion(ctx, v); err != nil {
			return err
		}
	}
	// 没有生效版本时默认 v3
	if _, err := db.ActivePromptVersion(ctx); err != nil {
		return db.ActivatePromptVersion(ctx, "prompt_v3")
	}
	return nil
}
