package httpapi

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"

	"github.com/llm-gateway/llm-gateway/internal/auth"
)

// login 用邮箱 + 密码换 JWT。
func (s *Server) login(c *gin.Context) {
	var in struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := c.ShouldBindJSON(&in); err != nil || strings.TrimSpace(in.Email) == "" || in.Password == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "invalid_request", "message": "email 与 password 必填"}})
		return
	}

	u, err := s.db.FindUserByEmail(c.Request.Context(), strings.ToLower(strings.TrimSpace(in.Email)))
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": gin.H{"code": "invalid_credentials", "message": "邮箱或密码不正确"}})
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(in.Password)) != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": gin.H{"code": "invalid_credentials", "message": "邮箱或密码不正确"}})
		return
	}

	token, exp, err := s.jwt.Issue(u.ID, u.Email, u.Name, u.Tenant, u.Role)
	if err != nil {
		s.fail(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"token":     token,
		"expiresAt": exp,
		"user": gin.H{
			"id":     u.ID,
			"email":  u.Email,
			"name":   u.Name,
			"tenant": u.Tenant,
			"role":   u.Role,
		},
	})
}

// me 返回当前登录身份。
func (s *Server) me(c *gin.Context) {
	claims, ok := auth.From(c.Request.Context())
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": gin.H{"code": "unauthorized", "message": "未登录"}})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"email":     claims.Email,
		"name":      claims.Name,
		"tenant":    claims.Tenant,
		"role":      claims.Role,
		"expiresAt": claims.ExpiresAt,
	})
}
