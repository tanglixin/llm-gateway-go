package store

import (
	"context"
	"fmt"
	"time"
)

// Document 是一份知识库文档及其 ETL 状态。
type Document struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	ACL        string    `json:"acl"`
	Status     string    `json:"status"`
	Stage      int       `json:"stage"`
	Chunks     int       `json:"chunks"`
	SizeBytes  int64     `json:"sizeBytes"`
	StorageKey string    `json:"storageKey,omitempty"`
	Error      string    `json:"error,omitempty"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

const docCols = `id::text, name, acl, status, stage, chunk_count, size_bytes, storage_key, error, updated_at`

func scanDoc(row interface {
	Scan(dest ...any) error
}) (*Document, error) {
	var d Document
	if err := row.Scan(&d.ID, &d.Name, &d.ACL, &d.Status, &d.Stage, &d.Chunks,
		&d.SizeBytes, &d.StorageKey, &d.Error, &d.UpdatedAt); err != nil {
		return nil, err
	}
	return &d, nil
}

// CreateDocument 插入一条待处理文档。
func (s *Store) CreateDocument(ctx context.Context, tenant, name, acl, storageKey string, size int64) (*Document, error) {
	row := s.pool.QueryRow(ctx, `
		INSERT INTO documents (tenant_id, name, acl, status, stage, storage_key, size_bytes)
		VALUES ($1, $2, $3, 'uploaded', 0, $4, $5)
		RETURNING `+docCols, tenant, name, acl, storageKey, size)
	d, err := scanDoc(row)
	if err != nil {
		return nil, fmt.Errorf("store: create document: %w", err)
	}
	return d, nil
}

// ListDocuments 返回租户下的文档（新的在前）。
func (s *Store) ListDocuments(ctx context.Context, tenant string) ([]Document, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+docCols+` FROM documents WHERE tenant_id = $1 ORDER BY created_at DESC LIMIT 100`, tenant)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]Document, 0, 16)
	for rows.Next() {
		d, err := scanDoc(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *d)
	}
	return out, rows.Err()
}

// GetDocument 取单条。
func (s *Store) GetDocument(ctx context.Context, id string) (*Document, error) {
	d, err := scanDoc(s.pool.QueryRow(ctx, `SELECT `+docCols+` FROM documents WHERE id = $1::uuid`, id))
	if err != nil {
		if err.Error() == "no rows in result set" {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return d, nil
}

// SetDocumentStage 更新 ETL 阶段与状态。
func (s *Store) SetDocumentStage(ctx context.Context, id string, stage int, status, errMsg string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE documents SET stage = $2, status = $3, error = $4, updated_at = now()
		WHERE id = $1::uuid`, id, stage, status, errMsg)
	return err
}

// ReplaceChunks 重建某文档的分块（重试时先删后插）。
func (s *Store) ReplaceChunks(ctx context.Context, docID, tenant string, items []ChunkInput) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `DELETE FROM chunks WHERE document_id = $1::uuid`, docID); err != nil {
		return err
	}
	for _, it := range items {
		if _, err := tx.Exec(ctx, `
			INSERT INTO chunks (document_id, tenant_id, idx, content, tokens, embedding)
			VALUES ($1::uuid, $2, $3, $4, $5, $6::vector)`,
			docID, tenant, it.Idx, it.Content, it.Tokens, vectorLiteral(it.Embedding),
		); err != nil {
			return fmt.Errorf("store: replace chunk: %w", err)
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE documents SET chunk_count = $2 WHERE id = $1::uuid`, docID, len(items)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// NormalizeReadyDocuments 把已入库但没有阶段标记的文档补成「就绪」。
// 用于升级前灌入的种子数据。
func (s *Store) NormalizeReadyDocuments(ctx context.Context, tenant string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE documents SET stage = 5, status = 'ready'
		WHERE tenant_id = $1 AND stage = 0 AND chunk_count > 0 AND status = 'ready'`, tenant)
	return err
}
