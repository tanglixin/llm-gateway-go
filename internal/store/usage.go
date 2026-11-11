package store

import (
	"context"
	"time"
)

// ModelUsage 是按模型聚合的用量。
type ModelUsage struct {
	Model      string  `json:"model"`
	Requests   int64   `json:"requests"`
	TokensIn   int64   `json:"tokensIn"`
	TokensOut  int64   `json:"tokensOut"`
	CostCNY    float64 `json:"cost"`
	AvgLatency int64   `json:"avgLatencyMs"`
}

// TopRequest 是一条调用记录。
type TopRequest struct {
	At        time.Time `json:"at"`
	Tenant    string    `json:"tenant"`
	TraceID   string    `json:"traceId"`
	Preview   string    `json:"preview"`
	Model     string    `json:"model"`
	Tokens    int64     `json:"tokens"`
	CostCNY   float64   `json:"cost"`
	LatencyMs int       `json:"latencyMs"`
}

// DailyPoint 是某天的用量。
type DailyPoint struct {
	Day      string  `json:"day"`
	Requests int64   `json:"requests"`
	Tokens   int64   `json:"tokens"`
	CostCNY  float64 `json:"cost"`
}

// UsageSummary 是看板用的汇总。
type UsageSummary struct {
	Requests    int64        `json:"requests"`
	TokensIn    int64        `json:"tokensIn"`
	TokensOut   int64        `json:"tokensOut"`
	TokensTotal int64        `json:"tokensTotal"`
	CostCNY     float64      `json:"cost"`
	AvgLatency  int64        `json:"avgLatencyMs"`
	P95Latency  int64        `json:"p95LatencyMs"`
	Models      []ModelUsage `json:"models"`
	Top         []TopRequest `json:"top"`
	Daily       []DailyPoint `json:"daily"`
}

// 定价（元 / 百万 token），与前端保持一致。
const (
	pricePromptPerM     = 0.14
	priceCompletionPerM = 0.28
)

// Usage 统计租户的调用情况。since 为零值时统计全部。
func (s *Store) Usage(ctx context.Context, tenant string, since time.Time) (*UsageSummary, error) {
	args := []any{tenant}
	cond := `m.role = 'assistant' AND c.tenant_id = $1`
	if !since.IsZero() {
		cond += ` AND m.created_at >= $2`
		args = append(args, since)
	}
	const from = ` FROM messages m JOIN conversations c ON c.id = m.conversation_id WHERE `

	out := &UsageSummary{Models: []ModelUsage{}, Top: []TopRequest{}, Daily: []DailyPoint{}}

	// 汇总
	err := s.pool.QueryRow(ctx, `
		SELECT count(*), coalesce(sum(m.tokens_in),0), coalesce(sum(m.tokens_out),0),
		       coalesce(avg(m.latency_ms),0)::bigint`+from+cond, args...).
		Scan(&out.Requests, &out.TokensIn, &out.TokensOut, &out.AvgLatency)
	if err != nil {
		return nil, err
	}
	out.TokensTotal = out.TokensIn + out.TokensOut
	out.CostCNY = cost(out.TokensIn, out.TokensOut)

	// P95
	_ = s.pool.QueryRow(ctx, `
		SELECT coalesce(percentile_cont(0.95) WITHIN GROUP (ORDER BY m.latency_ms), 0)::bigint`+from+cond+` AND m.latency_ms > 0`, args...).Scan(&out.P95Latency)

	// 按模型
	rows, err := s.pool.Query(ctx, `
		SELECT coalesce(nullif(m.model,''),'unknown') AS mm, count(*),
		       coalesce(sum(m.tokens_in),0), coalesce(sum(m.tokens_out),0), coalesce(avg(m.latency_ms),0)::bigint`+from+cond+`
		GROUP BY mm ORDER BY count(*) DESC`, args...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var m ModelUsage
		if err := rows.Scan(&m.Model, &m.Requests, &m.TokensIn, &m.TokensOut, &m.AvgLatency); err != nil {
			rows.Close()
			return nil, err
		}
		m.CostCNY = cost(m.TokensIn, m.TokensOut)
		out.Models = append(out.Models, m)
	}
	rows.Close()

	// 最近调用
	rows, err = s.pool.Query(ctx, `
		SELECT m.created_at, coalesce(c.title,'') AS preview, m.model,
		       m.tokens_in + m.tokens_out, m.latency_ms, m.id::text`+from+cond+`
		ORDER BY m.created_at DESC LIMIT 5`, args...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var t TopRequest
		if err := rows.Scan(&t.At, &t.Preview, &t.Model, &t.Tokens, &t.LatencyMs, &t.TraceID); err != nil {
			rows.Close()
			return nil, err
		}
		t.Tenant = tenant
		t.CostCNY = cost(0, t.Tokens)
		out.Top = append(out.Top, t)
	}
	rows.Close()

	// 最近 7 天
	rows, err = s.pool.Query(ctx, `
		SELECT to_char(m.created_at, 'MM-DD') AS d, count(*),
		       coalesce(sum(m.tokens_in + m.tokens_out),0)`+from+cond+` AND m.created_at >= now() - interval '7 days'
		GROUP BY d ORDER BY d`, args...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var p DailyPoint
		if err := rows.Scan(&p.Day, &p.Requests, &p.Tokens); err != nil {
			rows.Close()
			return nil, err
		}
		p.CostCNY = cost(0, p.Tokens)
		out.Daily = append(out.Daily, p)
	}
	rows.Close()

	return out, nil
}

func cost(tokensIn, tokensOut int64) float64 {
	return float64(tokensIn)/1e6*pricePromptPerM + float64(tokensOut)/1e6*priceCompletionPerM
}
