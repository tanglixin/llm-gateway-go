package httpapi

import (
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// uploadDocument 接收文件（multipart）或文本（JSON），存 MinIO 后入队。
func (s *Server) uploadDocument(c *gin.Context) {
	var (
		name string
		data []byte
		acl  = "全员可见"
	)

	if strings.HasPrefix(c.GetHeader("Content-Type"), "multipart/form-data") {
		fh, err := c.FormFile("file")
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "invalid_request", "message": "file 字段缺失"}})
			return
		}
		f, err := fh.Open()
		if err != nil {
			s.fail(c, err)
			return
		}
		defer func(f multipart.File) { _ = f.Close() }(f)

		data, err = io.ReadAll(f)
		if err != nil {
			s.fail(c, err)
			return
		}
		name = fh.Filename
		if a := c.PostForm("acl"); a != "" {
			acl = a
		}
	} else {
		var in struct {
			Name    string `json:"name"`
			Content string `json:"content"`
			ACL     string `json:"acl"`
		}
		if err := c.ShouldBindJSON(&in); err != nil || in.Name == "" || in.Content == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "invalid_request", "message": "name 与 content 必填"}})
			return
		}
		name, data = in.Name, []byte(in.Content)
		if in.ACL != "" {
			acl = in.ACL
		}
	}

	if len(data) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "invalid_request", "message": "文件为空"}})
		return
	}

	ctx := c.Request.Context()
	key := fmt.Sprintf("%s/%d-%s", s.tenantOf(c), time.Now().UnixNano(), sanitize(name))

	if err := s.blob.Put(ctx, key, data, contentTypeOf(name)); err != nil {
		s.fail(c, err)
		return
	}

	doc, err := s.db.CreateDocument(ctx, s.tenantOf(c), name, acl, key, int64(len(data)))
	if err != nil {
		s.fail(c, err)
		return
	}
	if err := s.queue.Push(ctx, doc.ID); err != nil {
		_ = s.db.SetDocumentStage(ctx, doc.ID, 0, "failed", "入队失败: "+err.Error())
		s.fail(c, err)
		return
	}

	c.JSON(http.StatusCreated, doc)
}

// listDocuments 返回所有文档及 ETL 阶段。
func (s *Server) listDocuments(c *gin.Context) {
	docs, err := s.db.ListDocuments(c.Request.Context(), s.tenantOf(c))
	if err != nil {
		s.fail(c, err)
		return
	}
	depth, _ := s.queue.Depth(c.Request.Context())
	c.JSON(http.StatusOK, gin.H{"documents": docs, "stages": stages, "queueDepth": depth})
}

// getDocument 单条查询（前端轮询用）。
func (s *Server) getDocument(c *gin.Context) {
	id, ok := pathUUID(c)
	if !ok {
		return
	}
	doc, err := s.db.GetDocument(c.Request.Context(), id, s.tenantOf(c))
	if err != nil {
		s.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, doc)
}

// retryDocument 重新入队。
func (s *Server) retryDocument(c *gin.Context) {
	id, ok := pathUUID(c)
	if !ok {
		return
	}
	doc, err := s.db.GetDocument(c.Request.Context(), id, s.tenantOf(c))
	if err != nil {
		s.fail(c, err)
		return
	}
	if err := s.db.SetDocumentStage(c.Request.Context(), id, 0, "uploaded", ""); err != nil {
		s.fail(c, err)
		return
	}
	if err := s.queue.Push(c.Request.Context(), id); err != nil {
		s.fail(c, err)
		return
	}
	doc.Stage, doc.Status, doc.Error = 0, "uploaded", ""
	c.JSON(http.StatusOK, doc)
}

var stages = []map[string]any{
	{"i": 1, "k": "parse", "name": "解析", "desc": "提取正文，识别标题/表格"},
	{"i": 2, "k": "chunk", "name": "切分", "desc": "按段落切块，过短合并"},
	{"i": 3, "k": "embed", "name": "向量化", "desc": "nomic-embed-text 768 维"},
	{"i": 4, "k": "index", "name": "入库", "desc": "写 pgvector，建 HNSW 索引"},
	{"i": 5, "k": "ready", "name": "就绪", "desc": "可被检索与引用"},
}

func sanitize(name string) string {
	r := strings.NewReplacer("/", "_", "\\", "_", " ", "_")
	return r.Replace(name)
}

func contentTypeOf(name string) string {
	switch {
	case strings.HasSuffix(strings.ToLower(name), ".pdf"):
		return "application/pdf"
	case strings.HasSuffix(strings.ToLower(name), ".md"):
		return "text/markdown; charset=utf-8"
	default:
		return "text/plain; charset=utf-8"
	}
}
