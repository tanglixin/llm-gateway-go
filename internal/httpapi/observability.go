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

	series, err := s.db.Series(c.Request.Context(), s.cfg.Tenant, minutes)
	if err != nil {
		s.fail(c, err)
		return
	}
	recent, err := s.db.Recent(c.Request.Context(), s.cfg.Tenant, 10)
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
