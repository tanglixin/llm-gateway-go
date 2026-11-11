package httpapi

import (
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

// bootstrap 返回 12 个页面的初始数据。
func (s *Server) bootstrap(c *gin.Context) {
	c.JSON(http.StatusOK, s.data.Snapshot())
}

// pagesMeta 供 design 页渲染「页面 ↔ FR ↔ 周次」矩阵。
func (s *Server) pagesMeta(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"pages": pages})
}

type pageMeta struct {
	Key   string   `json:"key"`
	Title string   `json:"title"`
	FR    []string `json:"fr"`
	Week  string   `json:"week"`
	Group string   `json:"group"`
}

var pages = []pageMeta{
	{"overview", "控制台总览", []string{"FR-15", "FR-04", "FR-09", "FR-19"}, "W3", "工作台"},
	{"chat", "智能问答", []string{"FR-03", "FR-08", "FR-12", "FR-13", "FR-18"}, "W1/W3", "工作台"},
	{"docs", "知识库 / 文档 ETL", []string{"FR-06", "FR-07", "FR-17"}, "W1", "知识工程"},
	{"retrieval", "检索调试台", []string{"FR-08", "FR-09"}, "W1/W3", "知识工程"},
	{"agent", "Agent 工作台", []string{"FR-10", "FR-11", "FR-12"}, "W2", "智能体"},
	{"gateway", "AI 网关", []string{"FR-01", "FR-02", "FR-05"}, "W2", "平台治理"},
	{"finops", "成本与用量", []string{"FR-04", "FR-17"}, "W2", "平台治理"},
	{"observability", "可观测与审计", []string{"FR-15", "FR-16"}, "W3", "平台治理"},
	{"eval", "评测中心", []string{"FR-09", "FR-13"}, "W3", "平台治理"},
	{"security", "安全与租户", []string{"FR-14", "FR-17"}, "W3", "平台治理"},
	{"ops", "部署运维", []string{"FR-19"}, "W4", "平台治理"},
	{"design", "信息架构与设计规范", []string{"FR-18"}, "W3", "交付说明"},
}

// uploadDocument 模拟上传：新文档从「已上传」阶段开始。
func (s *Server) uploadDocument(c *gin.Context) {
	var in struct {
		Name string `json:"name"`
	}
	_ = c.ShouldBindJSON(&in)
	name := in.Name
	if name == "" {
		name = fmt.Sprintf("upload_%d.pdf", time.Now().Unix())
	}

	doc := map[string]any{
		"id":     fmt.Sprintf("doc_%d", time.Now().UnixNano()),
		"n":      name,
		"t":      "Acme 研发中心",
		"s":      "1.0 MB",
		"chunks": 0,
		"st":     "解析中",
		"stage":  1,
		"idx":    "idx_20261109_01",
		"acl":    "全员可见",
	}
	s.data.AddDocument(doc)
	c.JSON(http.StatusCreated, doc)
}

// retryDocument 重试失败解析。
func (s *Server) retryDocument(c *gin.Context) {
	id := c.Param("id")
	doc, ok := s.data.RetryDocument(id)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"code": "not_found", "message": "document not found"}})
		return
	}
	c.JSON(http.StatusOK, doc)
}

// retrievalSearch 按查询词做一次朴素打分，返回片段。
func (s *Server) retrievalSearch(c *gin.Context) {
	var in struct {
		Query     string  `json:"query"`
		Mode      string  `json:"mode"`
		TopK      int     `json:"topK"`
		Threshold float64 `json:"threshold"`
	}
	_ = c.ShouldBindJSON(&in)
	if in.Mode == "" {
		in.Mode = "hybrid"
	}
	if in.TopK <= 0 {
		in.TopK = 5
	}

	hits := s.data.Search(in.Query, in.Mode, in.TopK)
	c.JSON(http.StatusOK, gin.H{
		"mode":      in.Mode,
		"topK":      in.TopK,
		"threshold": in.Threshold,
		"hits":      hits,
		"tookMs":    12,
	})
}

// agentRun 返回一次 ReAct 轨迹（当前是回放种子数据）。
func (s *Server) agentRun(c *gin.Context) {
	var in struct {
		Task string `json:"task"`
	}
	_ = c.ShouldBindJSON(&in)
	steps := s.data.AgentSteps()
	c.JSON(http.StatusOK, gin.H{"task": in.Task, "runId": fmt.Sprintf("run_%d", time.Now().Unix()%10000), "steps": steps})
}

// gatewayDrill 打开/关闭故障演练，返回演练期间的 provider 状态。
func (s *Server) gatewayDrill(c *gin.Context) {
	var in struct {
		Enable bool `json:"enable"`
	}
	_ = c.ShouldBindJSON(&in)
	c.JSON(http.StatusOK, gin.H{
		"enabled":   in.Enable,
		"providers": s.data.Providers(in.Enable),
		"message":   map[bool]string{true: "熔断器 OPEN，流量已切到备用模型", false: "演练已重置"}[in.Enable],
	})
}

// evalRun 触发一次回归（同步返回结果，真实实现应改成异步任务）。
func (s *Server) evalRun(c *gin.Context) {
	var in struct {
		Version string `json:"version"`
	}
	_ = c.ShouldBindJSON(&in)
	if in.Version == "" {
		in.Version = "prompt_v3"
	}
	c.JSON(http.StatusOK, gin.H{
		"version":      in.Version,
		"total":        128,
		"passed":       116,
		"faithfulness": 0.914,
		"baseline":     0.89,
		"recallAt5":    0.93,
		"gate":         "passed",
		"tookSeconds":  96,
		"finishedAt":   time.Now().Format(time.RFC3339),
	})
}

// opsBackup 触发备份。
func (s *Server) opsBackup(c *gin.Context) {
	c.JSON(http.StatusAccepted, gin.H{
		"id":      fmt.Sprintf("bak_%s", time.Now().Format("20060102_150405")),
		"status":  "running",
		"started": time.Now().Format(time.RFC3339),
	})
}

// opsRestore 恢复演练。
func (s *Server) opsRestore(c *gin.Context) {
	var in struct {
		BackupID string `json:"backupId"`
	}
	_ = c.ShouldBindJSON(&in)
	c.JSON(http.StatusOK, gin.H{
		"backupId":    in.BackupID,
		"status":      "verified",
		"tookSeconds": 252,
		"message":     "干净数据卷 → 解密 → pg_restore → 索引校验 → 问答回归",
	})
}

// opsChecklist 勾选上线检查项。
func (s *Server) opsChecklist(c *gin.Context) {
	var in struct {
		Done bool `json:"done"`
	}
	_ = c.ShouldBindJSON(&in)
	c.JSON(http.StatusOK, gin.H{"id": c.Param("id"), "done": in.Done})
}
