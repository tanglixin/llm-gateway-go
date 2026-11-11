package store

import (
	"context"
	"errors"
	"fmt"
)

// User 是登录用户。
type User struct {
	ID           string `json:"id"`
	Email        string `json:"email"`
	Name         string `json:"name"`
	Tenant       string `json:"tenant"`
	Role         string `json:"role"`
	PasswordHash string `json:"-"`
}

// FindUserByEmail 按邮箱查用户。
func (s *Store) FindUserByEmail(ctx context.Context, email string) (*User, error) {
	var u User
	err := s.pool.QueryRow(ctx, `
		SELECT id::text, email, name, tenant_id, role, password_hash
		FROM users WHERE email = $1`, email).
		Scan(&u.ID, &u.Email, &u.Name, &u.Tenant, &u.Role, &u.PasswordHash)
	if err != nil {
		if err.Error() == "no rows in result set" {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &u, nil
}

// CountUsers 返回用户数。
func (s *Store) CountUsers(ctx context.Context) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&n)
	return n, err
}

// CreateUser 插入用户（已存在则跳过）。
func (s *Store) CreateUser(ctx context.Context, u User) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO users (email, name, tenant_id, role, password_hash)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (email) DO NOTHING`,
		u.Email, u.Name, u.Tenant, u.Role, u.PasswordHash)
	if err != nil {
		return fmt.Errorf("store: create user: %w", err)
	}
	return nil
}

// ErrInvalidCredentials 表示账号或密码错误。
var ErrInvalidCredentials = errors.New("invalid credentials")
