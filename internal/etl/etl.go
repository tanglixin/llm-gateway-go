// Package etl 消费队列里的文档任务，跑解析 → 分块 → 向量化 → 入库。
package etl

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/ledongthuc/pdf"

	"github.com/llm-gateway/llm-gateway/internal/blob"
	"github.com/llm-gateway/llm-gateway/internal/kb"
	"github.com/llm-gateway/llm-gateway/internal/llm"
	"github.com/llm-gateway/llm-gateway/internal/queue"
	"github.com/llm-gateway/llm-gateway/internal/store"
	"github.com/llm-gateway/llm-gateway/pkg/token"
)

// 五个阶段，与前端展示一一对应。
const (
	StageUploaded = 0
	StageParse    = 1
	StageChunk    = 2
	StageEmbed    = 3
	StageIndex    = 4
	StageReady    = 5
)

var StageNames = []string{"已上传", "解析", "切分", "向量化", "入库", "就绪"}

// Worker 是 ETL 消费者。
type Worker struct {
	st     *store.Store
	blob   *blob.Client
	emb    *llm.Embedder
	q      *queue.Queue
	tenant string
	log    *slog.Logger
}

// New 构造 Worker。
func New(st *store.Store, b *blob.Client, emb *llm.Embedder, q *queue.Queue, tenant string, log *slog.Logger) *Worker {
	return &Worker{st: st, blob: b, emb: emb, q: q, tenant: tenant, log: log}
}

// Run 阻塞消费，直到 ctx 取消。
func (w *Worker) Run(ctx context.Context) {
	w.log.Info("etl worker started")
	for {
		if ctx.Err() != nil {
			return
		}
		id, err := w.q.Pop(ctx, 2*time.Second)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			w.log.Warn("etl pop failed", "error", err)
			time.Sleep(time.Second)
			continue
		}
		if id == "" {
			continue
		}
		if err := w.Process(ctx, id); err != nil {
			w.log.Error("etl failed", "document", id, "error", err)
		}
	}
}

// Process 处理一个文档。
func (w *Worker) Process(ctx context.Context, docID string) error {
	doc, err := w.st.GetDocument(ctx, docID, "")
	if err != nil {
		return fmt.Errorf("load document: %w", err)
	}

	fail := func(stage int, err error) error {
		_ = w.st.SetDocumentStage(context.WithoutCancel(ctx), docID, stage, "failed", err.Error())
		return err
	}

	// 1) 解析
	_ = w.st.SetDocumentStage(ctx, docID, StageParse, "parsing", "")
	raw, err := w.blob.Get(ctx, doc.StorageKey)
	if err != nil {
		return fail(StageParse, err)
	}
	text, err := extract(doc.Name, raw)
	if err != nil {
		return fail(StageParse, err)
	}
	if strings.TrimSpace(text) == "" {
		return fail(StageParse, fmt.Errorf("没有提取到文本（可能是扫描件）"))
	}

	// 2) 切分
	_ = w.st.SetDocumentStage(ctx, docID, StageChunk, "chunking", "")
	parts := kb.Chunks(text)
	if len(parts) == 0 {
		return fail(StageChunk, fmt.Errorf("切分结果为空"))
	}

	// 3) 向量化
	_ = w.st.SetDocumentStage(ctx, docID, StageEmbed, "embedding", "")
	items := make([]store.ChunkInput, 0, len(parts))
	for i, p := range parts {
		vec, err := w.emb.Embed(ctx, p)
		if err != nil {
			return fail(StageEmbed, err)
		}
		items = append(items, store.ChunkInput{Idx: i, Content: p, Tokens: token.Estimate(p), Embedding: vec})
	}

	// 4) 入库
	_ = w.st.SetDocumentStage(ctx, docID, StageIndex, "indexing", "")
	if err := w.st.ReplaceChunks(ctx, docID, w.tenant, items); err != nil {
		return fail(StageIndex, err)
	}

	// 5) 就绪
	_ = w.st.SetDocumentStage(ctx, docID, StageReady, "ready", "")
	w.log.Info("etl done", "document", doc.Name, "chunks", len(items))
	return nil
}

// extract 按扩展名提取文本。
func extract(name string, data []byte) (string, error) {
	lower := strings.ToLower(name)
	switch {
	case strings.HasSuffix(lower, ".pdf"):
		r, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			return "", fmt.Errorf("解析 PDF 失败: %w", err)
		}
		rd, err := r.GetPlainText()
		if err != nil {
			return "", fmt.Errorf("提取 PDF 文本失败: %w", err)
		}
		b, err := io.ReadAll(rd)
		if err != nil {
			return "", err
		}
		return string(b), nil
	case strings.HasSuffix(lower, ".docx"):
		return docxText(data)
	default:
		return string(data), nil
	}
}
