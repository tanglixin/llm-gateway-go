// Command server 是网关的 HTTP 入口。
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/llm-gateway/llm-gateway/internal/auth"
	"github.com/llm-gateway/llm-gateway/internal/blob"
	"github.com/llm-gateway/llm-gateway/internal/config"
	"github.com/llm-gateway/llm-gateway/internal/data"
	"github.com/llm-gateway/llm-gateway/internal/etl"
	"github.com/llm-gateway/llm-gateway/internal/gateway"
	"github.com/llm-gateway/llm-gateway/internal/httpapi"
	"github.com/llm-gateway/llm-gateway/internal/kb"
	"github.com/llm-gateway/llm-gateway/internal/llm"
	"github.com/llm-gateway/llm-gateway/internal/queue"
	"github.com/llm-gateway/llm-gateway/internal/store"
	"golang.org/x/crypto/bcrypt"
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

	gw, err := buildGateway(cfg)
	if err != nil {
		return err
	}
	log.Info("gateway ready", "providers", gw.Names())

	db, err := store.Open(context.Background(), cfg.PostgresDSN)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := db.Migrate(context.Background()); err != nil {
		return err
	}
	if err := seedUsers(context.Background(), db, log); err != nil {
		return err
	}
	log.Info("postgres ready", "dsn", cfg.PostgresDSN)

	emb, err := llm.NewEmbedder(cfg.OllamaBaseURL, cfg.OllamaEmbed)
	if err != nil {
		return err
	}
	if err := kb.Seed(context.Background(), db, emb, demoTenant); err != nil {
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
		Handler:           httpapi.New(cfg, auth.NewManager(cfg.JWTSecret, 12*time.Hour), gw, client, emb, data.New(), db, bl, q),
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

// buildGateway 按配置构造 provider 列表。
func buildGateway(cfg config.Config) (*gateway.Gateway, error) {
	providers := make([]*gateway.Provider, 0, len(cfg.Providers))
	for _, pc := range cfg.Providers {
		var (
			p   *gateway.Provider
			err error
		)
		switch pc.Kind {
		case "openai":
			p, err = gateway.NewOpenAIProvider(pc.Name, pc.BaseURL, pc.Token, pc.Model, pc.Weight)
		default:
			p, err = gateway.NewOllamaProvider(pc.Name, pc.BaseURL, pc.Model, pc.Weight)
		}
		if err != nil {
			return nil, err
		}
		providers = append(providers, p)
	}
	if len(providers) == 0 {
		return nil, fmt.Errorf("no providers configured")
	}
	return gateway.New(providers), nil
}

// demoTenant 是演示数据的归属租户，与种子用户一致。
const demoTenant = "acme-rd"

// seedUsers 首次启动时写入演示账号（两个租户，用于验证隔离）。
func seedUsers(ctx context.Context, db *store.Store, log *slog.Logger) error {
	n, err := db.CountUsers(ctx)
	if err != nil {
		return err
	}
	if n > 0 {
		return nil
	}

	seeds := []struct {
		email, name, tenant, role, password string
	}{
		{"tang@acme.dev", "唐立新", "acme-rd", "admin", "gateway"},
		{"demo@acme.dev", "演示用户", "demo-guest", "member", "gateway"},
	}
	for _, s := range seeds {
		hash, err := bcrypt.GenerateFromPassword([]byte(s.password), bcrypt.DefaultCost)
		if err != nil {
			return err
		}
		if err := db.CreateUser(ctx, store.User{
			Email: s.email, Name: s.name, Tenant: s.tenant, Role: s.role, PasswordHash: string(hash),
		}); err != nil {
			return err
		}
	}
	log.Info("seeded demo users", "count", len(seeds), "password", "gateway")
	return nil
}
