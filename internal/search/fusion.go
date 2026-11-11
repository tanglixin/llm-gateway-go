package search

import "sort"

// FuseScores 用加权分数融合（convex combination）合并两路结果。
//
// 先各自 min-max 归一化，再按 alpha 加权：score = alpha*词法 + (1-alpha)*向量。
// 与 RRF 的区别在于它保留了分数的相对差距，而不只看名次——
// 当某一路（通常是 BM25）明显更可靠时，RRF 会把它的优势抹平。
func FuseScores(alpha float64, lexical, semantic []Hit, k int) []Hit {
	if alpha < 0 {
		alpha = 0
	}
	if alpha > 1 {
		alpha = 1
	}

	lex := normalize(lexical)
	sem := normalize(semantic)

	merged := map[string]*Hit{}
	order := make([]string, 0, len(lex)+len(sem))

	add := func(src map[string]float64, hits []Hit, weight float64) {
		for _, h := range hits {
			cur, ok := merged[h.ID]
			if !ok {
				cp := h
				cp.Score = 0
				merged[h.ID] = &cp
				order = append(order, h.ID)
				cur = &cp
			}
			cur.Score += weight * src[h.ID]
		}
	}

	add(lex, lexical, alpha)
	add(sem, semantic, 1-alpha)

	out := make([]Hit, 0, len(merged))
	for _, id := range order {
		out = append(out, *merged[id])
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Score > out[b].Score })
	if k > 0 && len(out) > k {
		out = out[:k]
	}
	return out
}

// normalize 把分数映射到 [0,1]；全相同时统一给 1。
func normalize(hits []Hit) map[string]float64 {
	out := make(map[string]float64, len(hits))
	if len(hits) == 0 {
		return out
	}
	minS, maxS := hits[0].Score, hits[0].Score
	for _, h := range hits {
		if h.Score < minS {
			minS = h.Score
		}
		if h.Score > maxS {
			maxS = h.Score
		}
	}
	span := maxS - minS
	for _, h := range hits {
		if span <= 0 {
			out[h.ID] = 1
			continue
		}
		out[h.ID] = (h.Score - minS) / span
	}
	return out
}
