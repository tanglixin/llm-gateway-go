package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestIssueAndParse(t *testing.T) {
	m := NewManager("test-secret", time.Hour)

	token, _, err := m.Issue("u1", "a@b.c", "张三", "acme-rd", "admin")
	if err != nil {
		t.Fatalf("issue: %v", err)
	}

	claims, err := m.Parse(token)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if claims.Tenant != "acme-rd" || claims.Role != "admin" || claims.Email != "a@b.c" {
		t.Fatalf("claims 不对: %+v", claims)
	}
}

func TestParseRejectsWrongSecret(t *testing.T) {
	issuer := NewManager("secret-a", time.Hour)
	verifier := NewManager("secret-b", time.Hour)

	token, _, _ := issuer.Issue("u1", "a@b.c", "张三", "acme-rd", "admin")
	if _, err := verifier.Parse(token); err == nil {
		t.Fatal("换密钥后应该校验失败")
	}
}

func TestParseRejectsExpired(t *testing.T) {
	m := NewManager("s", -time.Minute)
	token, _, _ := m.Issue("u1", "a@b.c", "张三", "acme-rd", "admin")
	if _, err := m.Parse(token); err == nil {
		t.Fatal("过期 token 应该校验失败")
	}
}

// RBAC：member 访问管理员接口应 403，admin 应放行。
func TestRequireRole(t *testing.T) {
	gin.SetMode(gin.TestMode)
	m := NewManager("s", time.Hour)

	run := func(role string, guard gin.HandlerFunc) int {
		r := gin.New()
		r.GET("/admin", func(c *gin.Context) {
			// 模拟已登录：直接把 claims 放进 context
			ctx := context.WithValue(c.Request.Context(), ctxKey{}, &Claims{Tenant: "t", Role: role})
			c.Request = c.Request.WithContext(ctx)
			c.Next()
		}, guard, func(c *gin.Context) { c.Status(http.StatusOK) })

		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/admin", nil))
		return w.Code
	}

	if got := run("member", m.RequireRole("admin")); got != http.StatusForbidden {
		t.Fatalf("member 应 403，实际 %d", got)
	}
	if got := run("admin", m.RequireRole("admin")); got != http.StatusOK {
		t.Fatalf("admin 应 200，实际 %d", got)
	}
	if got := run("admin", m.RequireRole("admin", "owner")); got != http.StatusOK {
		t.Fatalf("多角色时应放行，实际 %d", got)
	}
}
