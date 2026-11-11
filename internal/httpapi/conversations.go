package httpapi

import (
	"context"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/llm-gateway/llm-gateway/internal/store"
)

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func (s *Server) listConversations(c *gin.Context) {
	list, err := s.db.ListConversations(c.Request.Context(), s.cfg.Tenant)
	if err != nil {
		s.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"conversations": list})
}

func (s *Server) createConversation(c *gin.Context) {
	var in struct {
		Title string `json:"title"`
		Model string `json:"model"`
	}
	_ = c.ShouldBindJSON(&in)
	model := in.Model
	if model == "" {
		model = s.llm.Model()
	}
	conv, err := s.db.CreateConversation(c.Request.Context(), s.cfg.Tenant, in.Title, model)
	if err != nil {
		s.fail(c, err)
		return
	}
	c.JSON(http.StatusCreated, conv)
}

func (s *Server) getConversation(c *gin.Context) {
	id, ok := pathUUID(c)
	if !ok {
		return
	}
	conv, err := s.db.GetConversation(c.Request.Context(), id)
	if err != nil {
		s.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, conv)
}

func (s *Server) renameConversation(c *gin.Context) {
	id, ok := pathUUID(c)
	if !ok {
		return
	}
	var in struct {
		Title string `json:"title"`
	}
	if err := c.ShouldBindJSON(&in); err != nil || strings.TrimSpace(in.Title) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "invalid_request", "message": "title is required"}})
		return
	}
	title := strings.TrimSpace(in.Title)
	if r := []rune(title); len(r) > 40 {
		title = string(r[:40])
	}
	if err := s.db.RenameConversation(c.Request.Context(), id, title); err != nil {
		s.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"id": id, "title": title})
}

func (s *Server) deleteConversation(c *gin.Context) {
	id, ok := pathUUID(c)
	if !ok {
		return
	}
	if err := s.db.DeleteConversation(c.Request.Context(), id); err != nil {
		s.fail(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// chat 调 Ollama 并把来回两条消息落库。
func (s *Server) chat(c *gin.Context) {
	var in struct {
		ConversationID string `json:"conversationId"`
		Prompt         string `json:"prompt"`
	}
	if err := c.ShouldBindJSON(&in); err != nil || strings.TrimSpace(in.Prompt) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "invalid_request", "message": "prompt is required"}})
		return
	}
	prompt := strings.TrimSpace(in.Prompt)
	ctx := c.Request.Context()

	convID := in.ConversationID
	if convID == "" {
		conv, err := s.db.CreateConversation(ctx, s.cfg.Tenant, "", s.llm.Model())
		if err != nil {
			s.fail(c, err)
			return
		}
		convID = conv.ID
	}

	// 客户端断开也要落库，用 WithoutCancel 保留值、去掉取消信号。
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()

	userMsg, err := s.db.AppendMessage(writeCtx, convID, store.Message{
		Role: "user", Content: prompt, Model: s.llm.Model(),
	})
	if err != nil {
		s.fail(c, err)
		return
	}

	start := time.Now()
	answer, usage, err := s.llm.Generate(ctx, prompt)
	latency := time.Since(start)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": gin.H{"code": "upstream_error", "message": err.Error()}})
		return
	}

	assistantMsg, err := s.db.AppendMessage(writeCtx, convID, store.Message{
		Role: "assistant", Content: answer, Model: s.llm.Model(),
		TokensIn: usage.Prompt, TokensOut: usage.Completion, LatencyMs: int(latency.Milliseconds()),
	})
	if err != nil {
		s.fail(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"conversationId": convID,
		"userMessageId":  userMsg.ID,
		"messageId":      assistantMsg.ID,
		"answer":         answer,
		"usage":          usage,
		"model":          s.llm.Model(),
		"latencyMs":      latency.Milliseconds(),
	})
}

func pathUUID(c *gin.Context) (string, bool) {
	id := c.Param("id")
	if !uuidPattern.MatchString(id) {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "invalid_id", "message": "invalid conversation id"}})
		return "", false
	}
	return id, true
}

func (s *Server) fail(c *gin.Context, err error) {
	if err == store.ErrNotFound {
		c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"code": "not_found", "message": "conversation not found"}})
		return
	}
	c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "internal_error", "message": err.Error()}})
}
