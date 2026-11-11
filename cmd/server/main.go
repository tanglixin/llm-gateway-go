// Command server 是网关的 HTTP 入口。
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/llm-gateway/llm-gateway/internal/blob"
	"github.com/llm-gateway/llm-gateway/internal/config"
	"github.com/llm-gateway/llm-gateway/internal/data"
	"github.com/llm-gateway/llm-gateway/internal/etl"
	"github.com/llm-gateway/llm-gateway/internal/httpapi"
	"github.com/llm-gateway/llm-gateway/internal/kb"
	"github.com/llm-gateway/llm-gateway/internal/llm"
	"github.com/llm-gateway/llm-gateway/internal/queue"
	"github.com/llm-gateway/llm-gateway/internal/store"
)

func main() {
	if err := run(); err != nil {
		slog.Error("server exited", "error", err)
		os.Exit(1)
	}
}

func run() error {
	cfg := config.Load()
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(log)

	client, err := llm.New(cfg.OllamaBaseURL, cfg.OllamaModel)
	if err != nil {
		return err
	}

	db, err := store.Open(context.Background(), cfg.PostgresDSN)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := db.Migrate(context.Background()); err != nil {
		return err
	}
	log.Info("postgres ready", "dsn", cfg.PostgresDSN)

	emb, err := llm.NewEmbedder(cfg.OllamaBaseURL, cfg.OllamaEmbed)
	if err != nil {
		return err
	}
	if err := kb.Seed(context.Background(), db, emb, cfg.Tenant); err != nil {
		log.Warn("knowledge base seed failed", "error", err)
	}

	// 对象存储：文档原件
	bl, err := blob.New(cfg.MinioEndpoint, cfg.MinioAccessKey, cfg.MinioSecretKey, cfg.MinioBucket, false)
	if err != nil {
		return err
	}
	if err := bl.EnsureBucket(context.Background()); err != nil {
		return err
	}
	log.Info("minio ready", "endpoint", cfg.MinioEndpoint, "bucket", cfg.MinioBucket)

	// 任务队列 + ETL worker
	q, err := queue.New(cfg.RedisAddr, "etl:jobs")
	if err != nil {
		return err
	}
	defer q.Close()
	log.Info("redis ready", "addr", cfg.RedisAddr)

	worker := etl.New(db, bl, emb, q, cfg.Tenant, log)
	workerCtx, stopWorker := context.WithCancel(context.Background())
	defer stopWorker()
	go worker.Run(workerCtx)

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           httpapi.New(cfg, client, emb, data.New(), db, bl, q),
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Info("llm-gateway listening",
			"addr", cfg.Addr, "ollama", cfg.OllamaBaseURL, "model", cfg.OllamaModel)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}
