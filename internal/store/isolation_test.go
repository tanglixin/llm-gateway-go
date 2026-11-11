package store_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/llm-gateway/llm-gateway/internal/store"
)

// 需要真实数据库：TEST_POSTGRES_DSN=postgres://llm:llm@127.0.0.1:5432/llm?sslmode=disable go test ./internal/store/
func openDB(t *testing.T) *store.Store {
	t.Helper()
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN 未设置，跳过集成测试")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	db, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(db.Close)
	return db
}

// 跨租户读取必须拿不到数据 —— 这是之前真实存在的漏洞。
func TestConversationTenantIsolation(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()

	owner, err := db.CreateConversation(ctx, "iso-a", "隔离测试", "qwen2:0.5b")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	t.Cleanup(func() { _ = db.DeleteConversation(ctx, owner.ID, "iso-a") })

	if _, err := db.GetConversation(ctx, owner.ID, "iso-a"); err != nil {
		t.Fatalf("本租户应能读到: %v", err)
	}

	if _, err := db.GetConversation(ctx, owner.ID, "iso-b"); err != store.ErrNotFound {
		t.Fatalf("跨租户应返回 ErrNotFound，实际: %v", err)
	}
	if err := db.DeleteConversation(ctx, owner.ID, "iso-b"); err != store.ErrNotFound {
		t.Fatalf("跨租户删除应失败，实际: %v", err)
	}
	if err := db.RenameConversation(ctx, owner.ID, "iso-b", "hacked"); err != store.ErrNotFound {
		t.Fatalf("跨租户改名应失败，实际: %v", err)
	}

	// 内部调用（tenant 为空）仍能读到
	if _, err := db.GetConversation(ctx, owner.ID, ""); err != nil {
		t.Fatalf("内部调用应能读到: %v", err)
	}
}

func TestDocumentTenantIsolation(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()

	doc, err := db.CreateDocument(ctx, "iso-a", "隔离文档.md", "全员可见", "iso-a/key", 12)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	if _, err := db.GetDocument(ctx, doc.ID, "iso-a"); err != nil {
		t.Fatalf("本租户应能读到: %v", err)
	}
	if _, err := db.GetDocument(ctx, doc.ID, "iso-b"); err != store.ErrNotFound {
		t.Fatalf("跨租户应返回 ErrNotFound，实际: %v", err)
	}
}
