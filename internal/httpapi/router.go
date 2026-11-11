// Package httpapi 提供 HTTP 接口与前端静态资源。
package httpapi

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/llm-gateway/llm-gateway/internal/auth"
	"github.com/llm-gateway/llm-gateway/internal/blob"
	"github.com/llm-gateway/llm-gateway/internal/config"
	"github.com/llm-gateway/llm-gateway/internal/data"
	"github.com/llm-gateway/llm-gateway/internal/gateway"
	"github.com/llm-gateway/llm-gateway/internal/llm"
	"github.com/llm-gateway/llm-gateway/internal/queue"
	"github.com/llm-gateway/llm-gateway/internal/search"
	"github.com/llm-gateway/llm-gateway/internal/store"
)

// Server 聚合 handler 依赖。
type Server struct {
	cfg   config.Config
	jwt   *auth.Manager
	gw    *gateway.Gateway
	llm   *llm.Client
	emb   *llm.Embedder
	data  *data.Store
	db    *store.Store
	blob  *blob.Client
	queue *queue.Queue

	ixMu sync.Mutex
	ix   *search.Index
	ixN  int
}

// New 构造路由。
func New(cfg config.Config, jwtMgr *auth.Manager, gw *gateway.Gateway, client *llm.Client, emb *llm.Embedder, st *data.Store, db *store.Store, bl *blob.Client, q *queue.Queue) *gin.Engine {
	s := &Server{cfg: cfg, jwt: jwtMgr, gw: gw, llm: client, emb: emb, data: st, db: db, blob: bl, queue: q}

	r := gin.New()
	r.Use(gin.Logger(), gin.Recovery())

	// /api/* 需要登录；健康检查与登录本身公开
	api := r.Group("/api", s.jwt.Middleware())

	r.GET("/healthz", s.healthz)
	r.GET("/readyz", s.readyz)
	r.POST("/api/auth/login", s.login)

	// 12 个页面的初始数据
	api.GET("/auth/me", s.me)
	api.GET("/bootstrap", s.bootstrap)
	api.GET("/meta/pages", s.pagesMeta)
	api.GET("/usage/summary", s.usageSummary)
	api.GET("/observability/series", s.observability)
	api.GET("/gateway/status", s.gatewayStatus)

	// 会话（持久化）
	api.GET("/conversations", s.listConversations)
	api.POST("/conversations", s.createConversation)
	api.GET("/conversations/:id", s.getConversation)
	api.PATCH("/conversations/:id", s.renameConversation)
	api.DELETE("/conversations/:id", s.deleteConversation)

	// 动作类
	api.POST("/chat", s.chat)
	api.GET("/documents", s.listDocuments)
	api.POST("/documents", s.uploadDocument)
	api.GET("/documents/:id", s.getDocument)
	api.POST("/documents/:id/retry", s.retryDocument)
	api.POST("/retrieval/search", s.retrievalSearch)
	api.POST("/retrieval/evaluate", s.retrievalEvaluate)
	api.POST("/agent/run", s.agentRun)
	api.POST("/gateway/drill", s.gatewayDrill)
	api.POST("/eval/run", s.evalRun)
	api.POST("/ops/backup", s.opsBackup)
	api.POST("/ops/restore", s.opsRestore)
	api.PATCH("/ops/checklist/:id", s.opsChecklist)

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
	if err := s.gw.Healthy(ctx); err != nil {
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

// tenantOf 从 token 里取租户；没有身份时回落到默认租户（仅用于公开接口）。
func (s *Server) tenantOf(c *gin.Context) string {
	if t, ok := auth.TenantFrom(c.Request.Context()); ok {
		return t
	}
	return s.cfg.Tenant
}
