package store

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// ChunkInput 是待写入的分块。
type ChunkInput struct {
	Idx       int
	Content   string
	Tokens    int
	Embedding []float32
}

// Chunk 是检索命中的分块。
type Chunk struct {
	ID         string  `json:"id"`
	DocumentID string  `json:"documentId"`
	Name       string  `json:"name"`
	Idx        int     `json:"idx"`
	Content    string  `json:"content"`
	Score      float64 `json:"score"`
}

// CountDocuments 返回租户下的文档数。
func (s *Store) CountDocuments(ctx context.Context, tenant string) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `SELECT count(*) FROM documents WHERE tenant_id = $1`, tenant).Scan(&n)
	return n, err
}

// InsertDocument 插入文档，返回 id。
func (s *Store) InsertDocument(ctx context.Context, tenant, name, acl string) (string, error) {
	var id string
	err := s.pool.QueryRow(ctx, `
		INSERT INTO documents (tenant_id, name, acl) VALUES ($1, $2, $3)
		RETURNING id::text`, tenant, name, acl).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("store: insert document: %w", err)
	}
	return id, nil
}

// InsertChunks 写入分块并回填 chunk_count。
func (s *Store) InsertChunks(ctx context.Context, docID, tenant string, items []ChunkInput) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	for _, it := range items {
		if _, err := tx.Exec(ctx, `
			INSERT INTO chunks (document_id, tenant_id, idx, content, tokens, embedding)
			VALUES ($1::uuid, $2, $3, $4, $5, $6::vector)`,
			docID, tenant, it.Idx, it.Content, it.Tokens, vectorLiteral(it.Embedding),
		); err != nil {
			return fmt.Errorf("store: insert chunk: %w", err)
		}
	}

	if _, err := tx.Exec(ctx, `UPDATE documents SET chunk_count = $2 WHERE id = $1::uuid`, docID, len(items)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// SearchChunks 按余弦相似度检索。
func (s *Store) SearchChunks(ctx context.Context, tenant string, emb []float32, limit int) ([]Chunk, error) {
	if limit <= 0 {
		limit = 5
	}
	rows, err := s.pool.Query(ctx, `
		SELECT c.id::text, c.document_id::text, d.name, c.idx, c.content,
		       1 - (c.embedding <=> $1::vector) AS score
		FROM chunks c
		JOIN documents d ON d.id = c.document_id
		WHERE c.tenant_id = $2
		ORDER BY c.embedding <=> $1::vector
		LIMIT $3`, vectorLiteral(emb), tenant, limit)
	if err != nil {
		return nil, fmt.Errorf("store: search chunks: %w", err)
	}
	defer rows.Close()

	var out []Chunk
	for rows.Next() {
		var c Chunk
		if err := rows.Scan(&c.ID, &c.DocumentID, &c.Name, &c.Idx, &c.Content, &c.Score); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// vectorLiteral 把 []float32 转成 pgvector 的文本表示："[0.1,0.2]"。
func vectorLiteral(v []float32) string {
	var sb strings.Builder
	sb.WriteByte('[')
	for i, f := range v {
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteString(strconv.FormatFloat(float64(f), 'f', 6, 32))
	}
	sb.WriteByte(']')
	return sb.String()
}
