package httpapi

import (
	"context"
	"math"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/llm-gateway/llm-gateway/internal/search"
)

// bm25Index 返回当前语料的 BM25 索引；分块数变化时自动重建。
func (s *Server) bm25Index(ctx context.Context) (*search.Index, error) {
	chunks, err := s.db.AllChunks(ctx, s.cfg.Tenant)
	if err != nil {
		return nil, err
	}

	s.ixMu.Lock()
	defer s.ixMu.Unlock()
	if s.ix == nil || s.ixN != len(chunks) {
		docs := make([]search.Doc, len(chunks))
		for i, c := range chunks {
			docs[i] = search.Doc{ID: c.ID, Name: c.Name, Idx: c.Idx, Content: c.Content}
		}
		s.ix = search.New(docs)
		s.ixN = len(chunks)
	}
	return s.ix, nil
}

// searchAll 按模式检索，返回统一结构。
func (s *Server) searchAll(ctx context.Context, query, mode string, topK int, alpha float64) ([]search.Hit, error) {
	ix, err := s.bm25Index(ctx)
	if err != nil {
		return nil, err
	}

	switch mode {
	case "bm25":
		hits := ix.Search(query, topK)
		for i := range hits {
			hits[i].Source = "bm25"
		}
		return hits, nil

	case "vector":
		vec, err := s.emb.Embed(ctx, query)
		if err != nil {
			return nil, err
		}
		chunks, err := s.db.SearchChunks(ctx, s.cfg.Tenant, vec, topK)
		if err != nil {
			return nil, err
		}
		hits := make([]search.Hit, 0, len(chunks))
		for _, c := range chunks {
			hits = append(hits, search.Hit{
				Doc:    search.Doc{ID: c.ID, Name: c.Name, Idx: c.Idx, Content: c.Content},
				Score:  c.Score,
				Source: "vector",
			})
		}
		return hits, nil

	default: // hybrid
		lexical := ix.Search(query, 20)

		vec, err := s.emb.Embed(ctx, query)
		if err != nil {
			return nil, err
		}
		chunks, err := s.db.SearchChunks(ctx, s.cfg.Tenant, vec, 20)
		if err != nil {
			return nil, err
		}
		semantic := make([]search.Hit, 0, len(chunks))
		for _, c := range chunks {
			semantic = append(semantic, search.Hit{
				Doc:   search.Doc{ID: c.ID, Name: c.Name, Idx: c.Idx, Content: c.Content},
				Score: c.Score,
			})
		}

		fused := search.FuseScores(alpha, lexical, semantic, topK)
		for i := range fused {
			fused[i].Source = "hybrid"
		}
		return fused, nil
	}
}

// defaultAlpha 是词法权重：分数融合里 BM25 占 0.6、向量占 0.4。
// 取值来自 evaluate 接口的扫描结果，不是拍脑袋。
const defaultAlpha = 0.6

// retrievalEvaluate 用内置评测集跑三种模式，返回真实指标。
//
// hybrid 额外扫描 alpha（词法权重），因为两者的最优配比取决于语料。
func (s *Server) retrievalEvaluate(c *gin.Context) {
	const topK = 5
	cases := search.GoldenSet
	alphas := []float64{0, 0.3, 0.5, 0.6, 0.7, 0.8, 1}

	type pair struct{ lexical, semantic []search.Hit }
	perQuery := make([]pair, 0, len(cases))

	ix, err := s.bm25Index(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"message": err.Error()}})
		return
	}

	var embedTotal int64
	for _, gc := range cases {
		t0 := time.Now()
		vec, err := s.emb.Embed(c.Request.Context(), gc.Query)
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": gin.H{"message": err.Error()}})
			return
		}
		embedTotal += time.Since(t0).Milliseconds()

		lexical := ix.Search(gc.Query, 20)
		chunks, err := s.db.SearchChunks(c.Request.Context(), s.cfg.Tenant, vec, 20)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"message": err.Error()}})
			return
		}
		semantic := make([]search.Hit, 0, len(chunks))
		for _, ch := range chunks {
			semantic = append(semantic, search.Hit{Doc: search.Doc{ID: ch.ID, Name: ch.Name, Content: ch.Content}})
		}
		perQuery = append(perQuery, pair{lexical: lexical, semantic: semantic})
	}

	type metrics struct {
		recall, rr, ndcg float64
		ms               int64
	}

	score := func(alpha float64, hybrid bool) metrics {
		var out metrics
		for i, gc := range cases {
			var hits []search.Hit
			switch {
			case alpha == 1:
				hits = perQuery[i].lexical
			case alpha == 0:
				hits = perQuery[i].semantic
			default:
				hits = search.FuseScores(alpha, perQuery[i].lexical, perQuery[i].semantic, topK)
			}
			if len(hits) > topK {
				hits = hits[:topK]
			}
			rank := 0
			for j, h := range hits {
				if h.Name == gc.Doc {
					rank = j + 1
					break
				}
			}
			if rank > 0 {
				out.recall++
				out.rr += 1 / float64(rank)
				out.ndcg += 1 / math.Log2(float64(rank)+1)
			}
		}
		n := float64(len(cases))
		out.recall /= n
		out.rr /= n
		out.ndcg /= n
		_ = hybrid
		return out
	}

	modes := []gin.H{}
	for _, m := range []struct {
		mode  string
		label string
		alpha float64
	}{
		{"bm25", "BM25 稀疏", 1},
		{"vector", "向量稠密", 0},
		{"hybrid", "混合（加权融合）", defaultAlpha},
	} {
		v := score(m.alpha, m.mode == "hybrid")
		modes = append(modes, gin.H{
			"mode":   m.mode,
			"label":  m.label,
			"recall": math.Round(v.recall*100) / 100,
			"mrr":    math.Round(v.rr*100) / 100,
			"ndcg":   math.Round(v.ndcg*100) / 100,
			"avgMs":  v.ms,
		})
	}

	sweep := make([]gin.H, 0, len(alphas))
	for _, a := range alphas {
		v := score(a, true)
		sweep = append(sweep, gin.H{
			"alpha":  a,
			"recall": math.Round(v.recall*100) / 100,
			"mrr":    math.Round(v.rr*100) / 100,
			"ndcg":   math.Round(v.ndcg*100) / 100,
		})
	}

	// 逐条明细：哪条查询没命中、命中的排第几
	details := make([]gin.H, 0, len(cases))
	for i, gc := range cases {
		rankOf := func(hits []search.Hit) int {
			if len(hits) > topK {
				hits = hits[:topK]
			}
			for j, h := range hits {
				if h.Name == gc.Doc {
					return j + 1
				}
			}
			return 0
		}
		details = append(details, gin.H{
			"query":  gc.Query,
			"doc":    gc.Doc,
			"bm25":   rankOf(perQuery[i].lexical),
			"vector": rankOf(perQuery[i].semantic),
			"hybrid": rankOf(search.FuseScores(defaultAlpha, perQuery[i].lexical, perQuery[i].semantic, topK)),
		})
	}

	c.JSON(http.StatusOK, gin.H{
		"cases":       len(cases),
		"topK":        topK,
		"modes":       modes,
		"fusionSweep": sweep,
		"details":     details,
		"embedAvgMs":  embedTotal / int64(len(cases)),
	})
}
