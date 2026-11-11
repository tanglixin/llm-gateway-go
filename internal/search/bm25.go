// Package search 提供 BM25 词法检索与结果融合。
package search

import (
	"math"
	"sort"
	"strings"
	"unicode"
)

// Doc 是参与检索的分块。
type Doc struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Idx     int    `json:"idx"`
	Content string `json:"content"`
}

// Hit 是一条命中。
type Hit struct {
	Doc
	Score  float64 `json:"score"`
	Source string  `json:"source,omitempty"`
}

const (
	k1 = 1.5
	b  = 0.75
)

// Index 是内存版 BM25 倒排索引。
//
// 语料很小（KB 级），全量放进内存最省事；
// 上规模后应换成独立的搜索引擎或 Postgres 的 tsvector。
type Index struct {
	docs  []Doc
	tf    []map[string]int
	dl    []int
	df    map[string]int
	avgdl float64
	total int
}

// New 用给定语料构建索引。
func New(docs []Doc) *Index {
	ix := &Index{
		docs:  docs,
		tf:    make([]map[string]int, len(docs)),
		dl:    make([]int, len(docs)),
		df:    make(map[string]int, 256),
		total: len(docs),
	}

	var sumLen int
	for i, d := range docs {
		toks := Tokenize(d.Content)
		m := make(map[string]int, len(toks))
		for _, t := range toks {
			m[t]++
		}
		ix.tf[i] = m
		ix.dl[i] = len(toks)
		sumLen += len(toks)
		for t := range m {
			ix.df[t]++
		}
	}
	if len(docs) > 0 {
		ix.avgdl = float64(sumLen) / float64(len(docs))
	}
	return ix
}

// Size 返回语料规模。
func (ix *Index) Size() int { return ix.total }

// Search 返回 top-k 命中（分数降序）。
func (ix *Index) Search(query string, k int) []Hit {
	if ix.total == 0 {
		return nil
	}
	qTokens := Tokenize(query)
	if len(qTokens) == 0 {
		return nil
	}

	scores := make([]float64, ix.total)
	for _, qt := range qTokens {
		df := ix.df[qt]
		if df == 0 {
			continue
		}
		idf := math.Log(1 + (float64(ix.total)-float64(df)+0.5)/(float64(df)+0.5))
		for i := range ix.docs {
			tf := float64(ix.tf[i][qt])
			if tf == 0 {
				continue
			}
			norm := tf + k1*(1-b+b*float64(ix.dl[i])/ix.avgdl)
			scores[i] += idf * tf * (k1 + 1) / norm
		}
	}

	hits := make([]Hit, 0, ix.total)
	for i, sc := range scores {
		if sc > 0 {
			hits = append(hits, Hit{Doc: ix.docs[i], Score: sc})
		}
	}
	sort.Slice(hits, func(a, b int) bool { return hits[a].Score > hits[b].Score })
	if k > 0 && len(hits) > k {
		hits = hits[:k]
	}
	return hits
}

// Tokenize 把文本切成检索词。
//
// 中文没有空格：用字符 bigram（「一线城市」→ 一线/线城/城市），
// 英文/数字按连续字母数字切，统一小写。
func Tokenize(s string) []string {
	var out []string
	var word strings.Builder

	flushWord := func() {
		if word.Len() > 0 {
			out = append(out, strings.ToLower(word.String()))
			word.Reset()
		}
	}

	runes := []rune(s)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		switch {
		case unicode.IsLetter(r) && r < 128, unicode.IsDigit(r):
			word.WriteRune(r)
		case isCJK(r):
			flushWord()
			if i+1 < len(runes) && isCJK(runes[i+1]) {
				out = append(out, string([]rune{r, runes[i+1]}))
			}
			out = append(out, string(r))
		default:
			flushWord()
		}
	}
	flushWord()
	return out
}

func isCJK(r rune) bool {
	return unicode.Is(unicode.Han, r)
}

// RRF 用 Reciprocal Rank Fusion 合并多路结果。
// score = Σ 1/(k + rank)，rank 从 1 开始。
func RRF(k int, lists ...[]Hit) []Hit {
	const offset = 60
	merged := map[string]*Hit{}
	order := make([]string, 0, 32)

	for _, list := range lists {
		for rank, h := range list {
			key := h.ID
			cur, ok := merged[key]
			if !ok {
				cp := h
				cp.Score = 0
				merged[key] = &cp
				order = append(order, key)
				cur = &cp
			}
			merged[key].Score += 1.0 / float64(offset+rank+1)
			_ = cur
		}
	}

	out := make([]Hit, 0, len(merged))
	for _, key := range order {
		out = append(out, *merged[key])
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Score > out[b].Score })
	if k > 0 && len(out) > k {
		out = out[:k]
	}
	return out
}
