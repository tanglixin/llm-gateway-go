package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// Message 是一条对话消息。
type Message struct {
	ID        string    `json:"id"`
	Role      string    `json:"role"`
	Content   string    `json:"content"`
	Model     string    `json:"model,omitempty"`
	TokensIn  int       `json:"tokensIn"`
	TokensOut int       `json:"tokensOut"`
	LatencyMs int       `json:"latencyMs"`
	CreatedAt time.Time `json:"createdAt"`
}

// Conversation 是一次会话。
type Conversation struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Model     string    `json:"model"`
	Preview   string    `json:"preview"`
	UpdatedAt time.Time `json:"updatedAt"`
	Messages  []Message `json:"messages,omitempty"`
	Count     int       `json:"count"`
}

const convCols = `id::text, title, model, preview, updated_at`

func scanConv(row pgx.Row) (*Conversation, error) {
	var c Conversation
	if err := row.Scan(&c.ID, &c.Title, &c.Model, &c.Preview, &c.UpdatedAt); err != nil {
		return nil, err
	}
	return &c, nil
}

// ListConversations 返回会话列表（不含消息，带条数）。
func (s *Store) ListConversations(ctx context.Context, tenant string) ([]Conversation, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+convCols+`, (SELECT count(*) FROM messages m WHERE m.conversation_id = c.id)
		FROM conversations c
		WHERE tenant_id = $1
		ORDER BY updated_at DESC
		LIMIT 100`, tenant)
	if err != nil {
		return nil, fmt.Errorf("store: list conversations: %w", err)
	}
	defer rows.Close()

	out := make([]Conversation, 0, 16)
	for rows.Next() {
		var c Conversation
		if err := rows.Scan(&c.ID, &c.Title, &c.Model, &c.Preview, &c.UpdatedAt, &c.Count); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// GetConversation 取会话及其消息。
func (s *Store) GetConversation(ctx context.Context, id string) (*Conversation, error) {
	c, err := scanConv(s.pool.QueryRow(ctx, `SELECT `+convCols+` FROM conversations WHERE id = $1::uuid`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: get conversation: %w", err)
	}

	rows, err := s.pool.Query(ctx, `
		SELECT id::text, role, content, model, tokens_in, tokens_out, latency_ms, created_at
		FROM messages WHERE conversation_id = $1::uuid ORDER BY created_at, id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var m Message
		if err := rows.Scan(&m.ID, &m.Role, &m.Content, &m.Model,
			&m.TokensIn, &m.TokensOut, &m.LatencyMs, &m.CreatedAt); err != nil {
			return nil, err
		}
		c.Messages = append(c.Messages, m)
	}
	c.Count = len(c.Messages)
	return c, rows.Err()
}

// CreateConversation 新建会话。
func (s *Store) CreateConversation(ctx context.Context, tenant, title, model string) (*Conversation, error) {
	if title == "" {
		title = "新对话"
	}
	c, err := scanConv(s.pool.QueryRow(ctx, `
		INSERT INTO conversations (tenant_id, title, model) VALUES ($1, $2, $3)
		RETURNING `+convCols, tenant, title, model))
	if err != nil {
		return nil, fmt.Errorf("store: create conversation: %w", err)
	}
	return c, nil
}

// AppendMessage 追加消息并更新会话的预览与时间。
func (s *Store) AppendMessage(ctx context.Context, convID string, m Message) (*Message, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	err = tx.QueryRow(ctx, `
		INSERT INTO messages (conversation_id, role, content, model, tokens_in, tokens_out, latency_ms)
		VALUES ($1::uuid, $2, $3, $4, $5, $6, $7)
		RETURNING id::text, created_at`,
		convID, m.Role, m.Content, m.Model, m.TokensIn, m.TokensOut, m.LatencyMs).Scan(&m.ID, &m.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("store: insert message: %w", err)
	}

	preview := m.Content
	if r := []rune(preview); len(r) > 60 {
		preview = string(r[:60])
	}
	if _, err := tx.Exec(ctx, `
		UPDATE conversations
		SET preview = $2, updated_at = now(),
		    title = CASE WHEN title = '新对话' AND $3 = 'user' THEN $4 ELSE title END
		WHERE id = $1::uuid`,
		convID, preview, m.Role, firstLine(m.Content)); err != nil {
		return nil, fmt.Errorf("store: touch conversation: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &m, nil
}

// RenameConversation 改名。
func (s *Store) RenameConversation(ctx context.Context, id, title string) error {
	tag, err := s.pool.Exec(ctx, `UPDATE conversations SET title = $2, updated_at = now() WHERE id = $1::uuid`, id, title)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteConversation 删除会话（消息级联）。
func (s *Store) DeleteConversation(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM conversations WHERE id = $1::uuid`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func firstLine(s string) string {
	r := []rune(s)
	if len(r) > 20 {
		return string(r[:20]) + "…"
	}
	return s
}
