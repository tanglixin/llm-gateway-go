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

	"github.com/llm-gateway/llm-gateway/internal/config"
	"github.com/llm-gateway/llm-gateway/internal/data"
	"github.com/llm-gateway/llm-gateway/internal/httpapi"
	"github.com/llm-gateway/llm-gateway/internal/kb"
	"github.com/llm-gateway/llm-gateway/internal/llm"
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

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           httpapi.New(cfg, client, emb, data.New(), db),
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
