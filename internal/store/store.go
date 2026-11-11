// Package store 封装 PostgreSQL 访问（会话与消息）。
package store

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNotFound 表示记录不存在。
var ErrNotFound = errors.New("store: not found")

//go:embed migrations/*.sql
var migrationFS embed.FS

// Store 持有连接池。
type Store struct {
	pool *pgxpool.Pool
}

// Open 建立连接池（含重试，容忍数据库比进程晚就绪）。
func Open(ctx context.Context, dsn string) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("store: parse dsn: %w", err)
	}
	cfg.MaxConns = 10
	cfg.MinConns = 1
	cfg.MaxConnLifetime = time.Hour

	var pool *pgxpool.Pool
	var lastErr error
	for i := 0; i < 30; i++ {
		pool, lastErr = pgxpool.NewWithConfig(ctx, cfg)
		if lastErr == nil {
			if pingErr := pool.Ping(ctx); pingErr == nil {
				return &Store{pool: pool}, nil
			} else {
				lastErr = pingErr
				pool.Close()
			}
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return nil, fmt.Errorf("store: connect after 30 attempts: %w", lastErr)
}

// Close 关闭连接池。
func (s *Store) Close() {
	if s != nil && s.pool != nil {
		s.pool.Close()
	}
}

// Ping 探活。
func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

// Migrate 按文件名顺序执行未应用的迁移。
func (s *Store) Migrate(ctx context.Context) error {
	if _, err := s.pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version    text PRIMARY KEY,
			applied_at timestamptz NOT NULL DEFAULT now()
		)`); err != nil {
		return fmt.Errorf("store: create schema_migrations: %w", err)
	}

	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("store: read migrations: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	for _, name := range names {
		var exists bool
		if err := s.pool.QueryRow(ctx,
			`SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version = $1)`, name).Scan(&exists); err != nil {
			return fmt.Errorf("store: check migration %s: %w", name, err)
		}
		if exists {
			continue
		}
		body, err := migrationFS.ReadFile("migrations/" + name)
		if err != nil {
			return fmt.Errorf("store: read migration %s: %w", name, err)
		}

		tx, err := s.pool.Begin(ctx)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, string(body)); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("store: apply migration %s: %w", name, err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations (version) VALUES ($1)`, name); err != nil {
			_ = tx.Rollback(ctx)
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
	}
	return nil
}
