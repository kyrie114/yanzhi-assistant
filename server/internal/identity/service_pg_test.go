package identity

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"

	"yanzhi/server/internal/apperr"
	"yanzhi/server/internal/platform/db"
)

type fixedClock struct{ now time.Time }

func (c fixedClock) Now() time.Time { return c.now }

func TestSameSecondPasswordChangeRevokesIssuedTokensInPG(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL is required")
	}
	ctx := context.Background()
	migrations := os.Getenv("MIGRATIONS_DIR")
	if migrations == "" {
		migrations = filepath.Join("..", "..", "migrations")
	}
	if err := db.Migrate(ctx, url, migrations); err != nil {
		t.Fatal(err)
	}
	pool, err := db.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	now := time.Date(2026, 11, 27, 8, 0, 0, 0, time.UTC)
	svc, err := NewService(pool, []byte("test-secret-with-at-least-32-bytes"), bcrypt.MinCost, fixedClock{now: now})
	if err != nil {
		t.Fatal(err)
	}
	email := "sv-" + uuid.NewString()[:8] + "@example.edu.cn"
	u, err := svc.Register(ctx, email, "同秒改密", "Abc12345")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1::uuid`, db.MustUUID(u.ID))
	})

	login1, err := svc.Login(ctx, email, "Abc12345")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Authenticate(ctx, login1.AccessToken); err != nil {
		t.Fatalf("initial auth failed: %v", err)
	}

	// Rotate password in the exact same second (fixedClock)
	if err := svc.ChangePassword(ctx, u.ID, "Abc12345", "Abc123456"); err != nil {
		t.Fatalf("change password failed: %v", err)
	}

	if _, err := svc.Authenticate(ctx, login1.AccessToken); !errors.Is(err, apperr.ErrUnauthenticated) {
		t.Fatalf("expected old token invalidated in same second, got err=%v", err)
	}

	login2, err := svc.Login(ctx, email, "Abc123456")
	if err != nil {
		t.Fatalf("new password login failed: %v", err)
	}
	if _, err := svc.Authenticate(ctx, login2.AccessToken); err != nil {
		t.Fatalf("new token auth failed: %v", err)
	}

	// Stale old password must be rejected
	if err := svc.ChangePassword(ctx, u.ID, "Abc12345", "Abc1234567"); !errors.Is(err, apperr.ErrCredentials) {
		t.Fatalf("expected stale password rejected, got %v", err)
	}
}
