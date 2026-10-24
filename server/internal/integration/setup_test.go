// Package integration 跑需要真实 PostgreSQL 的测试。
// 没有设置 TEST_DATABASE_URL 时全部跳过，单元测试仍可单独运行。
package integration

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"

	"yanzhi/server/internal/identity"
	"yanzhi/server/internal/knowledge"
	"yanzhi/server/internal/platform/clock"
	"yanzhi/server/internal/platform/db"
	"yanzhi/server/internal/platform/files"
)

var (
	testPool *pgxpool.Pool
	testURL  string
)

func TestMain(m *testing.M) {
	testURL = os.Getenv("TEST_DATABASE_URL")
	if testURL == "" {
		fmt.Fprintln(os.Stderr, "TEST_DATABASE_URL 未设置，跳过数据库集成测试")
		os.Exit(m.Run())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool, err := db.Connect(ctx, testURL)
	if err != nil {
		fmt.Fprintln(os.Stderr, "连接测试库失败:", err)
		os.Exit(1)
	}
	testPool = pool
	if _, err := pool.Exec(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public;`); err != nil {
		fmt.Fprintln(os.Stderr, "重置测试库失败:", err)
		os.Exit(1)
	}
	if err := db.Migrate(ctx, testURL, migrationsDir()); err != nil {
		fmt.Fprintln(os.Stderr, "迁移测试库失败:", err)
		os.Exit(1)
	}
	code := m.Run()
	pool.Close()
	os.Exit(code)
}

func migrationsDir() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..", "migrations")
}

func requireDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	if testPool == nil {
		t.Skip("TEST_DATABASE_URL 未设置")
	}
	return testPool
}

func newIdentity(t *testing.T) *identity.Service {
	t.Helper()
	svc, err := identity.NewService(requireDB(t), []byte("integration-secret"), bcrypt.MinCost, clock.System{})
	if err != nil {
		t.Fatalf("identity.NewService: %v", err)
	}
	return svc
}

func createUser(t *testing.T, prefix string) identity.PublicUser {
	t.Helper()
	svc := newIdentity(t)
	user, err := svc.Register(context.Background(), uniqueEmail(prefix), "张三", "Abc12345")
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	return user
}

func uniqueEmail(prefix string) string {
	return fmt.Sprintf("%s-%d@example.test", prefix, time.Now().UnixNano())
}

func newKnowledge(t *testing.T) knowledge.Service {
	t.Helper()
	return knowledge.Service{Pool: requireDB(t), Files: files.Store{Root: t.TempDir()}}
}

func createKB(t *testing.T, svc knowledge.Service, owner string) knowledge.KB {
	t.Helper()
	kb, err := svc.CreateKB(context.Background(), owner, "论文阅读")
	if err != nil {
		t.Fatalf("CreateKB: %v", err)
	}
	return kb
}

// tinyPDF 只需通过 pdfcheck 的头、页对象和加密检查，不要求真能渲染。
func tinyPDF(page int) []byte {
	return []byte(fmt.Sprintf("%%PDF-1.4\n1 0 obj<</Type /Page/MediaBox [0 0 612 792]>>endobj\n%% page %d\ntrailer<</Root 1 0 R>>\n%%%%EOF\n", page))
}
