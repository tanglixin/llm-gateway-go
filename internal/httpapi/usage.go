package httpapi

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

// usageSummary 返回真实用量：从 messages 表聚合。
func (s *Server) usageSummary(c *gin.Context) {
	since := time.Time{}
	if c.Query("range") == "today" {
		now := time.Now()
		since = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	}

	sum, err := s.db.Usage(c.Request.Context(), s.tenantOf(c), since)
	if err != nil {
		s.fail(c, err)
		return
	}

	// 顺带把知识库规模带上，总览页要用
	docs, _ := s.db.CountDocuments(c.Request.Context(), s.tenantOf(c))
	chunks, _ := s.db.AllChunks(c.Request.Context(), s.tenantOf(c))

	c.JSON(http.StatusOK, gin.H{
		"summary":   sum,
		"documents": docs,
		"chunks":    len(chunks),
		"range":     c.DefaultQuery("range", "all"),
	})
}
