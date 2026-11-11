package httpapi

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
)

// observability 返回按分钟的时间序列与最近调用，供可观测页作图。
func (s *Server) observability(c *gin.Context) {
	minutes, _ := strconv.Atoi(c.DefaultQuery("minutes", "60"))
	if minutes <= 0 || minutes > 24*60 {
		minutes = 60
	}

	series, err := s.db.Series(c.Request.Context(), s.tenantOf(c), minutes)
	if err != nil {
		s.fail(c, err)
		return
	}
	recent, err := s.db.Recent(c.Request.Context(), s.tenantOf(c), 10)
	if err != nil {
		s.fail(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"minutes": minutes,
		"series":  series,
		"recent":  recent,
	})
}

// listTraces 返回最近的 trace 概览。
func (s *Server) listTraces(c *gin.Context) {
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "10"))
	traces, err := s.db.RecentTraces(c.Request.Context(), limit)
	if err != nil {
		s.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"traces": traces})
}

// getTrace 返回某个 trace 的全部 span（前端据此画瀑布图）。
func (s *Server) getTrace(c *gin.Context) {
	spans, err := s.db.TraceSpans(c.Request.Context(), c.Param("traceId"))
	if err != nil {
		s.fail(c, err)
		return
	}
	if len(spans) == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"code": "not_found", "message": "trace not found"}})
		return
	}
	c.JSON(http.StatusOK, gin.H{"traceId": c.Param("traceId"), "spans": spans})
}
