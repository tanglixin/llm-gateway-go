package httpapi

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/llm-gateway/llm-gateway/internal/agent"
)

// agentRun 跑一次真实的 ReAct 循环，返回完整轨迹。
func (s *Server) agentRun(c *gin.Context) {
	var in struct {
		Task     string `json:"task"`
		MaxSteps int    `json:"maxSteps"`
	}
	_ = c.ShouldBindJSON(&in)
	task := strings.TrimSpace(in.Task)
	if task == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "invalid_request", "message": "task 必填"}})
		return
	}

	tools := agent.NewTools(s.db, s.emb, s.tenantOf(c))
	a := agent.New(s.gw, tools, in.MaxSteps)

	res, err := a.Run(c.Request.Context(), task)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": gin.H{"code": "agent_error", "message": err.Error()}, "steps": res.Steps})
		return
	}

	// 工具清单也返回给前端展示
	toolList := make([]gin.H, 0, len(tools))
	for _, t := range tools {
		toolList = append(toolList, gin.H{"name": t.Name, "desc": t.Desc, "spec": t.Spec})
	}

	c.JSON(http.StatusOK, gin.H{
		"task":       task,
		"steps":      res.Steps,
		"grounded":   res.Grounded,
		"unverified": res.Unverified,
		"tools":      toolList,
	})
}
