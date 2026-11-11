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
func (s *Server) searchAll(ctx context.Context, query, mode string, topK int) ([]search.Hit, error) {
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

		fused := search.RRF(topK, lexical, semantic)
		for i := range fused {
			fused[i].Source = "hybrid"
		}
		return fused, nil
	}
}

// retrievalEvaluate 用内置评测集跑三种模式，返回真实指标。
func (s *Server) retrievalEvaluate(c *gin.Context) {
	const topK = 5
	modes := []string{"bm25", "vector", "hybrid"}
	cases := search.GoldenSet

	// 预先算好每个查询的向量：向量化耗时单独统计，
	// 否则先跑的模式会把 embedding 时间算进去，后面模式反而"更快"。
	vecCache := map[string][]float32{}
	var embedTotal int64
	for _, gc := range cases {
		t0 := time.Now()
		v, err := s.emb.Embed(c.Request.Context(), gc.Query)
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": gin.H{"message": err.Error()}})
			return
		}
		embedTotal += time.Since(t0).Milliseconds()
		vecCache[gc.Query] = v
	}

	typeMode := map[string]struct {
		recall, rr, ndcg float64
		totalMs          int64
	}{}

	for _, mode := range modes {
		agg := struct {
			recall, rr, ndcg float64
			totalMs          int64
		}{}
		for _, gc := range cases {
			start := time.Now()

			var hits []search.Hit
			if mode == "bm25" {
				ix, err := s.bm25Index(c.Request.Context())
				if err != nil {
					c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"message": err.Error()}})
					return
				}
				hits = ix.Search(gc.Query, topK)
			} else {
				vec := vecCache[gc.Query]
				if mode == "vector" {
					chunks, err := s.db.SearchChunks(c.Request.Context(), s.cfg.Tenant, vec, topK)
					if err != nil {
						c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"message": err.Error()}})
						return
					}
					for _, ch := range chunks {
						hits = append(hits, search.Hit{Doc: search.Doc{ID: ch.ID, Name: ch.Name, Content: ch.Content}})
					}
				} else {
					ix, _ := s.bm25Index(c.Request.Context())
					lexical := ix.Search(gc.Query, 20)
					chunks, err := s.db.SearchChunks(c.Request.Context(), s.cfg.Tenant, vec, 20)
					if err != nil {
						c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"message": err.Error()}})
						return
					}
					sem := make([]search.Hit, 0, len(chunks))
					for _, ch := range chunks {
						sem = append(sem, search.Hit{Doc: search.Doc{ID: ch.ID, Name: ch.Name, Content: ch.Content}})
					}
					hits = search.RRF(topK, lexical, sem)
				}
			}

			agg.totalMs += time.Since(start).Milliseconds()

			rank := 0
			for i, h := range hits {
				if h.Name == gc.Doc {
					rank = i + 1
					break
				}
			}
			if rank > 0 {
				agg.recall += 1
				agg.rr += 1 / float64(rank)
				agg.ndcg += 1 / math.Log2(float64(rank)+1)
			}
		}

		n := float64(len(cases))
		typeMode[mode] = struct {
			recall, rr, ndcg float64
			totalMs          int64
		}{agg.recall / n, agg.rr / n, agg.ndcg / n, agg.totalMs / int64(len(cases))}
	}

	out := make([]gin.H, 0, len(modes))
	labels := map[string]string{
		"bm25":   "BM25 稀疏",
		"vector": "向量稠密",
		"hybrid": "混合 + RRF",
	}
	for _, m := range modes {
		v := typeMode[m]
		out = append(out, gin.H{
			"mode":   m,
			"label":  labels[m],
			"recall": math.Round(v.recall*100) / 100,
			"mrr":    math.Round(v.rr*100) / 100,
			"ndcg":   math.Round(v.ndcg*100) / 100,
			"avgMs":  v.totalMs,
		})
	}

	c.JSON(http.StatusOK, gin.H{
		"cases":      len(cases),
		"topK":       topK,
		"modes":      out,
		"embedAvgMs": embedTotal / int64(len(cases)),
	})
}
