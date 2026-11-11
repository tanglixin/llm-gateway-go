// Package httpapi 提供 HTTP 接口与前端静态资源。
package httpapi

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/llm-gateway/llm-gateway/internal/config"
	"github.com/llm-gateway/llm-gateway/internal/data"
	"github.com/llm-gateway/llm-gateway/internal/llm"
	"github.com/llm-gateway/llm-gateway/internal/store"
)

// Server 聚合 handler 依赖。
type Server struct {
	cfg  config.Config
	llm  *llm.Client
	data *data.Store
	db   *store.Store
}

// New 构造路由。
func New(cfg config.Config, client *llm.Client, st *data.Store, db *store.Store) *gin.Engine {
	s := &Server{cfg: cfg, llm: client, data: st, db: db}

	r := gin.New()
	r.Use(gin.Logger(), gin.Recovery())

	r.GET("/healthz", s.healthz)
	r.GET("/readyz", s.readyz)
	r.GET("/api/version", s.version)

	// 12 个页面的初始数据
	r.GET("/api/bootstrap", s.bootstrap)
	r.GET("/api/meta/pages", s.pagesMeta)

	// 会话（持久化）
	r.GET("/api/conversations", s.listConversations)
	r.POST("/api/conversations", s.createConversation)
	r.GET("/api/conversations/:id", s.getConversation)
	r.PATCH("/api/conversations/:id", s.renameConversation)
	r.DELETE("/api/conversations/:id", s.deleteConversation)

	// 动作类
	r.POST("/api/chat", s.chat)
	r.POST("/api/documents", s.uploadDocument)
	r.POST("/api/documents/:id/retry", s.retryDocument)
	r.POST("/api/retrieval/search", s.retrievalSearch)
	r.POST("/api/agent/run", s.agentRun)
	r.POST("/api/gateway/drill", s.gatewayDrill)
	r.POST("/api/eval/run", s.evalRun)
	r.POST("/api/ops/backup", s.opsBackup)
	r.POST("/api/ops/restore", s.opsRestore)
	r.PATCH("/api/ops/checklist/:id", s.opsChecklist)

	// 前端静态资源 + SPA 兜底
	r.NoRoute(s.serveWeb)
	return r
}

func (s *Server) healthz(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok", "service": "llm-gateway"})
}

func (s *Server) readyz(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
	defer cancel()
	if err := s.llm.Healthy(ctx); err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"status": "unavailable", "ollama": err.Error()})
		return
	}
	if err := s.db.Ping(ctx); err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"status": "unavailable", "postgres": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ready", "model": s.llm.Model()})
}

func (s *Server) version(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"service": "llm-gateway",
		"model":   s.llm.Model(),
		"ollama":  s.cfg.OllamaBaseURL,
	})
}

// serveWeb 托管 web/ 下的构建产物，找不到文件时回落到 index.html（SPA 路由）。
func (s *Server) serveWeb(c *gin.Context) {
	p := c.Request.URL.Path
	if strings.HasPrefix(p, "/api/") {
		c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"code": "not_found", "message": "no such endpoint: " + p}})
		return
	}

	rel := strings.TrimPrefix(p, "/")
	if rel == "" {
		rel = "index.html"
	}
	full := filepath.Join("web", filepath.Clean(rel))
	if !strings.HasPrefix(full, "web") {
		c.Status(http.StatusNotFound)
		return
	}
	if info, err := os.Stat(full); err == nil && !info.IsDir() {
		c.File(full)
		return
	}
	if _, err := os.Stat("web/index.html"); err != nil {
		c.String(http.StatusOK, "llm-gateway is running. web/ is empty — build the UI first.")
		return
	}
	c.File("web/index.html")
}
