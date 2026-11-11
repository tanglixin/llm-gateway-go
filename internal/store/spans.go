package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// SpanRow 是落库的一条 span。
type SpanRow struct {
	TraceID      string
	SpanID       string
	ParentSpanID string
	Name         string
	Service      string
	Kind         string
	StartAt      time.Time
	EndAt        time.Time
	DurationMs   int
	Status       string
	Attributes   map[string]any
}

// InsertSpans 批量写入 span。
func (s *Store) InsertSpans(ctx context.Context, rows []SpanRow) error {
	if len(rows) == 0 {
		return nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	for _, r := range rows {
		attrs, _ := json.Marshal(r.Attributes)
		if _, err := tx.Exec(ctx, `
			INSERT INTO spans (trace_id, span_id, parent_span_id, name, service, kind,
			                   start_at, end_at, duration_ms, status, attributes)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
			ON CONFLICT (trace_id, span_id) DO NOTHING`,
			r.TraceID, r.SpanID, r.ParentSpanID, r.Name, r.Service, r.Kind,
			r.StartAt, r.EndAt, r.DurationMs, r.Status, attrs); err != nil {
			return fmt.Errorf("store: insert span: %w", err)
		}
	}
	return tx.Commit(ctx)
}

// TraceSummary 是一次请求的概览。
type TraceSummary struct {
	TraceID    string    `json:"traceId"`
	RootName   string    `json:"rootName"`
	StartedAt  time.Time `json:"startedAt"`
	DurationMs int       `json:"durationMs"`
	Spans      int       `json:"spans"`
	Status     string    `json:"status"`
}

// SpanInfo 是给前端画瀑布图的 span。
type SpanInfo struct {
	TraceID      string         `json:"traceId"`
	SpanID       string         `json:"spanId"`
	ParentSpanID string         `json:"parentSpanId"`
	Name         string         `json:"name"`
	Kind         string         `json:"kind"`
	StartMs      int            `json:"startMs"`
	DurationMs   int            `json:"durationMs"`
	Status       string         `json:"status"`
	Attributes   map[string]any `json:"attributes"`
}

// RecentTraces 返回最近的 trace（按根 span 的起始时间倒序）。
func (s *Store) RecentTraces(ctx context.Context, limit int) ([]TraceSummary, error) {
	if limit <= 0 {
		limit = 10
	}
	rows, err := s.pool.Query(ctx, `
		SELECT trace_id,
		       coalesce(max(CASE WHEN parent_span_id = '' THEN name END), min(name)) AS root,
		       min(start_at) AS started,
		       (extract(epoch FROM max(end_at) - min(start_at)) * 1000)::int AS duration,
		       count(*) AS spans,
		       CASE WHEN bool_or(status = 'error') THEN 'error' ELSE 'ok' END AS status
		FROM spans
		GROUP BY trace_id
		ORDER BY started DESC
		LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]TraceSummary, 0, limit)
	for rows.Next() {
		var t TraceSummary
		if err := rows.Scan(&t.TraceID, &t.RootName, &t.StartedAt, &t.DurationMs, &t.Spans, &t.Status); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// TraceSpans 返回某个 trace 的全部 span（按起始时间排序）。
func (s *Store) TraceSpans(ctx context.Context, traceID string) ([]SpanInfo, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT trace_id, span_id, parent_span_id, name, kind,
		       (extract(epoch FROM start_at - min(start_at) OVER ()) * 1000)::int AS start_ms,
		       duration_ms, status, attributes
		FROM spans
		WHERE trace_id = $1
		ORDER BY start_at`, traceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]SpanInfo, 0, 16)
	for rows.Next() {
		var sp SpanInfo
		var attrs []byte
		if err := rows.Scan(&sp.TraceID, &sp.SpanID, &sp.ParentSpanID, &sp.Name, &sp.Kind,
			&sp.StartMs, &sp.DurationMs, &sp.Status, &attrs); err != nil {
			return nil, err
		}
		if len(attrs) > 0 {
			_ = json.Unmarshal(attrs, &sp.Attributes)
		}
		out = append(out, sp)
	}
	return out, rows.Err()
}
