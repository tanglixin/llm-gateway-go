package httpapi

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// gatewayStatus 返回各 provider 的真实状态（熔断、请求数、错误数）。
func (s *Server) gatewayStatus(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"providers": s.gw.Status(),
	})
}
