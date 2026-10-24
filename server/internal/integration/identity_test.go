package integration

import (
	"context"
	"testing"

	"yanzhi/server/internal/apperr"
)

func codeOf(err error) string {
	if e := apperr.As(err); e != nil {
		return e.Code
	}
	return ""
}

func TestIntegrationIdentityRegisterLoginLogout(t *testing.T) {
	requireDB(t)
	svc := newIdentity(t)
	ctx := context.Background()
	email := uniqueEmail("identity")

	user, err := svc.Register(ctx, email, "张三", "Abc12345")
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if user.Email != email || user.Role != "注册用户" {
		t.Fatalf("unexpected user: %+v", user)
	}
	if _, err := svc.Register(ctx, email, "李四", "Abc12345"); codeOf(err) != "EMAIL_TAKEN" {
		t.Fatalf("duplicate register: %v", err)
	}
	if _, err := svc.Login(ctx, email, "wrong-pass1"); codeOf(err) != "INVALID_CREDENTIALS" {
		t.Fatalf("wrong password: %v", err)
	}

	first, err := svc.Login(ctx, email, "Abc12345")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	second, err := svc.Login(ctx, email, "Abc12345")
	if err != nil {
		t.Fatalf("Login again: %v", err)
	}
	principal, err := svc.Authenticate(ctx, first.AccessToken)
	if err != nil || principal.UserID != user.ID {
		t.Fatalf("Authenticate: %+v %v", principal, err)
	}
	if principal.Role != "user" {
		t.Fatalf("token role should stay user, got %q", principal.Role)
	}

	if err := svc.Logout(ctx, principal); err != nil {
		t.Fatalf("Logout: %v", err)
	}
	if _, err := svc.Authenticate(ctx, first.AccessToken); codeOf(err) != "UNAUTHENTICATED" {
		t.Fatalf("revoked token should fail, got %v", err)
	}
	if _, err := svc.Authenticate(ctx, second.AccessToken); err != nil {
		t.Fatalf("other token must stay valid: %v", err)
	}

	if err := svc.ChangePassword(ctx, user.ID, "Abc12345", "Abc123456"); err != nil {
		t.Fatalf("ChangePassword: %v", err)
	}
	if _, err := svc.Login(ctx, email, "Abc12345"); codeOf(err) != "INVALID_CREDENTIALS" {
		t.Fatalf("old password should fail, got %v", err)
	}
	if _, err := svc.Authenticate(ctx, second.AccessToken); codeOf(err) != "UNAUTHENTICATED" {
		t.Fatalf("token issued before password change must fail, got %v", err)
	}
	if _, err := svc.Login(ctx, email, "Abc123456"); err != nil {
		t.Fatalf("new password login: %v", err)
	}
}

func TestIntegrationIdentityWeakPasswordKeepsUserOut(t *testing.T) {
	requireDB(t)
	svc := newIdentity(t)
	ctx := context.Background()
	email := uniqueEmail("weak")

	for _, password := range []string{"abc123", "abcdefgh"} {
		if _, err := svc.Register(ctx, email, "张三", password); codeOf(err) != "PASSWORD_WEAK" {
			t.Fatalf("password %q should be weak, got %v", password, err)
		}
	}
}
