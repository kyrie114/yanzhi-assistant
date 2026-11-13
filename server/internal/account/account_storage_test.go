package account

import (
	"errors"
	"testing"
	"time"

	"yanzhi/server/internal/apperr"
)

func TestMemoryStoreStorageOperations(t *testing.T) {
	store := NewMemoryStore()
	user := User{
		ID:           "test-user-id",
		Email:        "user@example.com",
		Name:         "User",
		Role:         roleUser,
		StorageBytes: 100,
	}
	if err := store.Create(user); err != nil {
		t.Fatal(err)
	}

	bytes, err := store.GetStorageBytes("test-user-id")
	if err != nil || bytes != 100 {
		t.Fatalf("expected 100 bytes, got %d, err %v", bytes, err)
	}

	if err := store.ReserveStorage("test-user-id", 50, 200); err != nil {
		t.Fatal(err)
	}
	bytes, _ = store.GetStorageBytes("test-user-id")
	if bytes != 150 {
		t.Fatalf("expected 150 bytes, got %d", bytes)
	}

	err = store.ReserveStorage("test-user-id", 60, 200)
	var appErr *apperr.Error
	if !errors.As(err, &appErr) || appErr.Code != "STORAGE_QUOTA_EXCEEDED" {
		t.Fatalf("expected STORAGE_QUOTA_EXCEEDED, got %v", err)
	}

	if err := store.ReleaseStorage("test-user-id", 100); err != nil {
		t.Fatal(err)
	}
	bytes, _ = store.GetStorageBytes("test-user-id")
	if bytes != 50 {
		t.Fatalf("expected 50 bytes, got %d", bytes)
	}

	if err := store.ReleaseStorage("test-user-id", 500); err != nil {
		t.Fatal(err)
	}
	bytes, _ = store.GetStorageBytes("test-user-id")
	if bytes != 0 {
		t.Fatalf("expected clamped 0 bytes, got %d", bytes)
	}

	if err := store.SetStorageBytes("test-user-id", 2137000000); err != nil {
		t.Fatal(err)
	}
	bytes, _ = store.GetStorageBytes("test-user-id")
	if bytes != 2137000000 {
		t.Fatalf("expected 2137000000 bytes, got %d", bytes)
	}

	now := time.Now().UTC()
	if err := store.SetEmailVerified("test-user-id", &now); err != nil {
		t.Fatal(err)
	}
	u, ok := store.FindByID("test-user-id")
	if !ok || u.EmailVerifiedAt == nil || !u.EmailVerifiedAt.Equal(now) {
		t.Fatalf("expected email verified at %v", now)
	}

	if err := store.SetEmailVerified("test-user-id", nil); err != nil {
		t.Fatal(err)
	}
	u, _ = store.FindByID("test-user-id")
	if u.EmailVerifiedAt != nil {
		t.Fatal("expected email verification cleared")
	}

	if _, err := store.GetStorageBytes("unknown"); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("expected ErrUnauthenticated for missing user, got %v", err)
	}
}

func TestServiceCreateAdmin(t *testing.T) {
	store := NewMemoryStore()
	svc := NewService(store, nil, []byte("test-jwt-secret-with-at-least-32-bytes"), 4)

	admin, err := svc.CreateAdmin("admin@example.com", "Admin User", "Admin123456")
	if err != nil {
		t.Fatal(err)
	}
	if admin.Role != roleAdmin {
		t.Fatalf("expected role admin, got %s", admin.Role)
	}
	if admin.EmailVerifiedAt == nil {
		t.Fatal("expected admin email to be verified automatically")
	}

	res, err := svc.Login("admin@example.com", "Admin123456")
	if err != nil {
		t.Fatal(err)
	}
	if res.Role != "系统管理员" {
		t.Fatalf("expected role label 系统管理员, got %s", res.Role)
	}
	if !res.EmailVerified {
		t.Fatal("expected admin to be marked email verified in login result")
	}
}
