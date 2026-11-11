package store

import (
	"context"
	"fmt"
	"time"
)

// PromptVersion 是一个可切换的提示词版本。
type PromptVersion struct {
	Name          string    `json:"name"`
	Description   string    `json:"description"`
	Template      string    `json:"template"`
	RetrievalMode string    `json:"retrievalMode"`
	TopK          int       `json:"topK"`
	IsActive      bool      `json:"isActive"`
	CreatedAt     time.Time `json:"createdAt"`
}

// EvalRun 是一次回归的结果。
type EvalRun struct {
	ID        int64     `json:"id"`
	Version   string    `json:"version"`
	Cases     int       `json:"cases"`
	Recall    float64   `json:"recall"`
	MRR       float64   `json:"mrr"`
	NDCG      float64   `json:"ndcg"`
	Gate      string    `json:"gate"`
	TookMs    int       `json:"tookMs"`
	CreatedAt time.Time `json:"createdAt"`
}

// UpsertPromptVersion 写入版本（已存在则更新）。
func (s *Store) UpsertPromptVersion(ctx context.Context, v PromptVersion) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO prompt_versions (name, description, template, retrieval_mode, top_k, is_active)
		VALUES ($1,$2,$3,$4,$5,$6)
		ON CONFLICT (name) DO UPDATE SET
			description = EXCLUDED.description,
			template = EXCLUDED.template,
			retrieval_mode = EXCLUDED.retrieval_mode,
			top_k = EXCLUDED.top_k`,
		v.Name, v.Description, v.Template, v.RetrievalMode, v.TopK, v.IsActive)
	if err != nil {
		return fmt.Errorf("store: upsert prompt version: %w", err)
	}
	return nil
}

// ListPromptVersions 返回全部版本（带最近一次评测）。
func (s *Store) ListPromptVersions(ctx context.Context) ([]PromptVersion, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT name, description, template, retrieval_mode, top_k, is_active, created_at
		FROM prompt_versions ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]PromptVersion, 0, 8)
	for rows.Next() {
		var v PromptVersion
		if err := rows.Scan(&v.Name, &v.Description, &v.Template, &v.RetrievalMode, &v.TopK, &v.IsActive, &v.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// ActivePromptVersion 返回当前生效的版本。
func (s *Store) ActivePromptVersion(ctx context.Context) (*PromptVersion, error) {
	var v PromptVersion
	err := s.pool.QueryRow(ctx, `
		SELECT name, description, template, retrieval_mode, top_k, is_active, created_at
		FROM prompt_versions WHERE is_active LIMIT 1`).
		Scan(&v.Name, &v.Description, &v.Template, &v.RetrievalMode, &v.TopK, &v.IsActive, &v.CreatedAt)
	if err != nil {
		return nil, ErrNotFound
	}
	return &v, nil
}

// ActivatePromptVersion 切换生效版本。
func (s *Store) ActivatePromptVersion(ctx context.Context, name string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `UPDATE prompt_versions SET is_active = false WHERE is_active`); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE prompt_versions SET is_active = true WHERE name = $1`, name)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return tx.Commit(ctx)
}

// InsertEvalRun 保存一次评测结果。
func (s *Store) InsertEvalRun(ctx context.Context, r EvalRun) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO eval_runs (version, cases, recall, mrr, ndcg, gate, took_ms)
		VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		r.Version, r.Cases, r.Recall, r.MRR, r.NDCG, r.Gate, r.TookMs)
	return err
}

// LatestEvalRuns 返回每个版本最近一次评测。
func (s *Store) LatestEvalRuns(ctx context.Context) ([]EvalRun, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT ON (version) id, version, cases, recall, mrr, ndcg, gate, took_ms, created_at
		FROM eval_runs ORDER BY version, created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]EvalRun, 0, 8)
	for rows.Next() {
		var r EvalRun
		if err := rows.Scan(&r.ID, &r.Version, &r.Cases, &r.Recall, &r.MRR, &r.NDCG, &r.Gate, &r.TookMs, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
