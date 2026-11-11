// Package auth 提供 JWT 签发/校验与请求上下文里的身份信息。
package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
)

// Claims 是 token 里携带的身份信息。
type Claims struct {
	Tenant string `json:"tenant"`
	Role   string `json:"role"`
	Name   string `json:"name"`
	Email  string `json:"email"`
	jwt.RegisteredClaims
}

// Manager 负责签发与校验 token。
type Manager struct {
	secret []byte
	ttl    time.Duration
}

// NewManager 构造 Manager。
func NewManager(secret string, ttl time.Duration) *Manager {
	return &Manager{secret: []byte(secret), ttl: ttl}
}

// Issue 签发一个 token。
func (m *Manager) Issue(userID, email, name, tenant, role string) (string, time.Time, error) {
	exp := time.Now().Add(m.ttl)
	claims := Claims{
		Tenant: tenant,
		Role:   role,
		Name:   name,
		Email:  email,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   userID,
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			ExpiresAt: jwt.NewNumericDate(exp),
			Issuer:    "llm-gateway",
		},
	}
	tok, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(m.secret)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("auth: sign: %w", err)
	}
	return tok, exp, nil
}

// Parse 校验并解析 token。
func (m *Manager) Parse(token string) (*Claims, error) {
	parsed, err := jwt.ParseWithClaims(token, &Claims{}, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method %v", t.Header["alg"])
		}
		return m.secret, nil
	})
	if err != nil {
		return nil, err
	}
	claims, ok := parsed.Claims.(*Claims)
	if !ok || !parsed.Valid {
		return nil, errors.New("auth: invalid token")
	}
	return claims, nil
}

type ctxKey struct{}

// From 从 context 取身份。
func From(ctx context.Context) (*Claims, bool) {
	c, ok := ctx.Value(ctxKey{}).(*Claims)
	return c, ok
}

// TenantFrom 从 context 取租户。
func TenantFrom(ctx context.Context) (string, bool) {
	c, ok := From(ctx)
	if !ok || c.Tenant == "" {
		return "", false
	}
	return c.Tenant, true
}

// Middleware 校验 Authorization: Bearer <token>。
func (m *Manager) Middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		raw := c.GetHeader("Authorization")
		if raw == "" {
			// 允许 SSE 用 query 参数传 token（EventSource 不能设头）
			raw = "Bearer " + c.Query("token")
		}
		const prefix = "Bearer "
		if !strings.HasPrefix(raw, prefix) {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error": gin.H{"code": "unauthorized", "message": "缺少 Authorization: Bearer <token>"},
			})
			return
		}

		claims, err := m.Parse(strings.TrimPrefix(raw, prefix))
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error": gin.H{"code": "unauthorized", "message": "token 无效或已过期"},
			})
			return
		}

		c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), ctxKey{}, claims))
		c.Set("claims", claims)
		c.Next()
	}
}

// Role 是从 context 取角色。
func RoleFrom(ctx context.Context) string {
	if c, ok := From(ctx); ok {
		return c.Role
	}
	return ""
}

// RequireRole 限制只有指定角色能访问；其余返回 403。
//
// 用独立中间件而不是在每个 handler 里判断：路由表上一眼能看出
// 哪些接口是管理员的，漏加也会在 review 时暴露。
func (m *Manager) RequireRole(roles ...string) gin.HandlerFunc {
	allowed := make(map[string]struct{}, len(roles))
	for _, r := range roles {
		allowed[r] = struct{}{}
	}

	return func(c *gin.Context) {
		claims, ok := From(c.Request.Context())
		if !ok {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error": gin.H{"code": "unauthorized", "message": "未登录"},
			})
			return
		}
		if _, ok := allowed[claims.Role]; !ok {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"error": gin.H{
					"code":    "forbidden",
					"message": "需要 " + joinRoles(roles) + " 角色，当前是 " + claims.Role,
				},
			})
			return
		}
		c.Next()
	}
}

func joinRoles(roles []string) string {
	out := ""
	for i, r := range roles {
		if i > 0 {
			out += "/"
		}
		out += r
	}
	return out
}
