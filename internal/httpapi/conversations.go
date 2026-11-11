package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
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

// chat 调 Ollama 并把来回两条消息落库；stream=true 时走 SSE。
func (s *Server) chat(c *gin.Context) {
	var in struct {
		ConversationID string `json:"conversationId"`
		Prompt         string `json:"prompt"`
		Stream         bool   `json:"stream"`
	}
	if err := c.ShouldBindJSON(&in); err != nil || strings.TrimSpace(in.Prompt) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "invalid_request", "message": "prompt is required"}})
		return
	}
	prompt := strings.TrimSpace(in.Prompt)

	convID := in.ConversationID
	if convID == "" {
		conv, err := s.db.CreateConversation(c.Request.Context(), s.cfg.Tenant, "", s.llm.Model())
		if err != nil {
			s.fail(c, err)
			return
		}
		convID = conv.ID
	}

	if in.Stream {
		s.chatStream(c, convID, prompt)
		return
	}
	s.chatOnce(c, convID, prompt)
}

// chatOnce 是非流式路径：等完整回答再返回。
func (s *Server) chatOnce(c *gin.Context, convID, prompt string) {
	ctx := c.Request.Context()

	// 用户消息先落库（短超时；此时还没有生成）
	userCtx, cancelUser := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	userMsg, err := s.db.AppendMessage(userCtx, convID, store.Message{
		Role: "user", Content: prompt, Model: s.gw.PrimaryModel(),
	})
	cancelUser()
	if err != nil {
		s.fail(c, err)
		return
	}

	// RAG：先检索，把片段拼进提示词
	cites, ctxText := s.retrieve(ctx, prompt, 3)

	start := time.Now()
	answer, usage, provider, err := s.gw.Generate(ctx, buildRAGPrompt(cites, ctxText, prompt))
	latency := time.Since(start)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": gin.H{"code": "upstream_error", "message": err.Error()}})
		return
	}

	// 助手消息在生成之后落库，必须重新给超时（生成可能已经跑了几十秒）
	asstCtx, cancelAsst := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancelAsst()
	assistantMsg, err := s.db.AppendMessage(asstCtx, convID, store.Message{
		Role: "assistant", Content: answer, Model: s.gw.PrimaryModel(),
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
		"model":          s.gw.PrimaryModel(),
		"provider":       provider,
		"latencyMs":      latency.Milliseconds(),
		"cites":          cites,
	})
}

// chatStream 是流式路径：边生成边推 SSE，结束后落库。
func (s *Server) chatStream(c *gin.Context, convID, prompt string) {
	ctx := c.Request.Context()

	userCtx, cancelUser := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	userMsg, err := s.db.AppendMessage(userCtx, convID, store.Message{
		Role: "user", Content: prompt, Model: s.gw.PrimaryModel(),
	})
	cancelUser()
	if err != nil {
		s.fail(c, err)
		return
	}

	h := c.Writer.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "no-cache, no-transform")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	h.Set("X-Conversation-Id", convID)
	h.Set("X-User-Message-Id", userMsg.ID)
	c.Writer.WriteHeader(http.StatusOK)
	c.Writer.Flush()

	cites, ctxText := s.retrieve(ctx, prompt, 3)

	start := time.Now()
	var firstToken time.Duration
	broken := false

	answer, usage, provider, err := s.gw.GenerateStream(ctx, buildRAGPrompt(cites, ctxText, prompt), func(delta string) error {
		if firstToken == 0 {
			firstToken = time.Since(start)
		}
		return sse(c, gin.H{
			"object": "chat.completion.chunk",
			"choices": []any{gin.H{
				"index": 0,
				"delta": gin.H{"content": delta},
			}},
		})
	})
	if err != nil {
		broken = true
		if ctx.Err() == nil {
			_ = sse(c, gin.H{"error": gin.H{"code": "upstream_error", "message": err.Error()}})
		}
	}

	latency := time.Since(start)

	// 落助手消息（中途断开也保存已生成的部分）；超时重新计算。
	asstCtx, cancelAsst := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancelAsst()
	if answer != "" || usage.Prompt > 0 {
		if _, perr := s.db.AppendMessage(asstCtx, convID, store.Message{
			Role: "assistant", Content: answer, Model: s.gw.PrimaryModel(),
			TokensIn: usage.Prompt, TokensOut: usage.Completion, LatencyMs: int(latency.Milliseconds()),
		}); perr != nil {
			log.Printf("persist assistant message failed: %v", perr)
		}
	}

	if !broken {
		_ = sse(c, gin.H{
			"object":  "chat.completion.chunk",
			"choices": []any{},
			"usage": gin.H{
				"prompt":     usage.Prompt,
				"completion": usage.Completion,
				"total":      usage.Total(),
			},
			"ttftMs":    firstToken.Milliseconds(),
			"latencyMs": latency.Milliseconds(),
			"model":     s.gw.PrimaryModel(),
			"provider":  provider,
			"cites":     cites,
		})
		_ = sseRaw(c, "[DONE]")
	}
}

// sse 写一帧 JSON 并 flush。
func sse(c *gin.Context, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return sseRaw(c, string(b))
}

func sseRaw(c *gin.Context, payload string) error {
	if _, err := fmt.Fprintf(c.Writer, "data: %s\n\n", payload); err != nil {
		return err
	}
	c.Writer.Flush()
	return nil
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
