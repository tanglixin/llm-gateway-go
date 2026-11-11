package store

import (
	"context"
	"time"
)

// Bucket 是按分钟聚合的一个点。
type Bucket struct {
	At       time.Time `json:"at"`
	Requests int64     `json:"requests"`
	AvgMs    int64     `json:"avgMs"`
	P95Ms    int64     `json:"p95Ms"`
	Tokens   int64     `json:"tokens"`
}

// Series 返回最近 minutes 分钟的按分钟时间序列。
func (s *Store) Series(ctx context.Context, tenant string, minutes int) ([]Bucket, error) {
	if minutes <= 0 {
		minutes = 60
	}
	rows, err := s.pool.Query(ctx, `
		SELECT date_trunc('minute', m.created_at) AS b,
		       count(*),
		       coalesce(avg(m.latency_ms),0)::bigint,
		       coalesce(percentile_cont(0.95) WITHIN GROUP (ORDER BY m.latency_ms),0)::bigint,
		       coalesce(sum(m.tokens_in + m.tokens_out),0)
		FROM messages m
		JOIN conversations c ON c.id = m.conversation_id
		WHERE m.role = 'assistant'
		  AND c.tenant_id = $1
		  AND m.created_at >= now() - make_interval(mins => $2)
		GROUP BY b ORDER BY b`, tenant, minutes)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]Bucket, 0, minutes)
	for rows.Next() {
		var b Bucket
		if err := rows.Scan(&b.At, &b.Requests, &b.AvgMs, &b.P95Ms, &b.Tokens); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// Recent 返回最近 n 条调用记录（给可观测页用）。
func (s *Store) Recent(ctx context.Context, tenant string, n int) ([]TopRequest, error) {
	if n <= 0 {
		n = 10
	}
	rows, err := s.pool.Query(ctx, `
		SELECT m.created_at, coalesce(c.title,''), m.model,
		       m.tokens_in + m.tokens_out, m.latency_ms, m.id::text
		FROM messages m
		JOIN conversations c ON c.id = m.conversation_id
		WHERE m.role = 'assistant' AND c.tenant_id = $1
		ORDER BY m.created_at DESC LIMIT $2`, tenant, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]TopRequest, 0, n)
	for rows.Next() {
		var t TopRequest
		if err := rows.Scan(&t.At, &t.Preview, &t.Model, &t.Tokens, &t.LatencyMs, &t.TraceID); err != nil {
			return nil, err
		}
		t.Tenant = tenant
		out = append(out, t)
	}
	return out, rows.Err()
}
