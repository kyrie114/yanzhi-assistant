package account

import (
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"yanzhi/server/internal/platform/clock"
)

func newSvc(clk clock.Clock) *Service {
	if clk == nil {
		clk = clock.System{}
	}
	return NewService(NewMemoryStore(), clk, []byte("test-secret"), bcrypt.MinCost)
}

func TestPasswordRule(t *testing.T) {
	if ValidPassword("abc123") || ValidPassword("abcdefgh") || ValidPassword("12345678") {
		t.Fatal("short, letters-only, and digits-only passwords are rejected")
	}
	if !ValidPassword("Abc12345") || !ValidPassword("密码abcd1234") {
		t.Fatal("8 characters with a letter and a digit are accepted")
	}
}

func TestRegisterStoresBcryptAndRejectsDuplicate(t *testing.T) {
	svc := newSvc(nil)
	user, err := svc.Register("A@Example.edu.cn", "张三", "Abc12345")
	if err != nil {
		t.Fatal(err)
	}
	if user.Email != "a@example.edu.cn" {
		t.Fatalf("email stored as %s", user.Email)
	}
	if user.Role != roleUser {
		t.Fatalf("role %s", user.Role)
	}
	if user.PasswordHash == "Abc12345" || !strings.HasPrefix(user.PasswordHash, "$2") {
		t.Fatalf("password was not stored as bcrypt: %s", user.PasswordHash)
	}
	if bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte("Abc12345")) != nil {
		t.Fatal("stored hash does not match the password")
	}
	if _, err := svc.Register("a@example.edu.cn", "李四", "Abc12345"); err != ErrEmailTaken {
		t.Fatalf("duplicate err %v", err)
	}
	if _, ok := svc.store.FindByEmail("a@example.edu.cn"); !ok {
		t.Fatal("original user missing")
	}
	if _, ok := svc.store.byEmail["a@example.edu.cn"]; !ok || len(svc.store.byID) != 1 {
		t.Fatalf("users = %d", len(svc.store.byID))
	}
}

func TestLoginDoesNotRevealWhetherEmailExists(t *testing.T) {
	svc := newSvc(nil)
	if _, err := svc.Register("a@example.edu.cn", "张三", "Abc12345"); err != nil {
		t.Fatal(err)
	}
	_, missing := svc.Login("missing@example.edu.cn", "Abc12345")
	_, wrong := svc.Login("a@example.edu.cn", "Abc123456")
	if missing != ErrInvalidCredentials || wrong != ErrInvalidCredentials {
		t.Fatalf("missing %v wrong %v", missing, wrong)
	}
}

func TestLogoutRevokesOnlyThatToken(t *testing.T) {
	svc := newSvc(nil)
	if _, err := svc.Register("a@example.edu.cn", "张三", "Abc12345"); err != nil {
		t.Fatal(err)
	}
	first, err := svc.Login("a@example.edu.cn", "Abc12345")
	if err != nil {
		t.Fatal(err)
	}
	second, err := svc.Login("a@example.edu.cn", "Abc12345")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Logout(first.AccessToken); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Authenticate(first.AccessToken); err != ErrUnauthenticated {
		t.Fatalf("logged out token: %v", err)
	}
	if _, err := svc.Authenticate(second.AccessToken); err != nil {
		t.Fatalf("other token: %v", err)
	}
}

func TestChangePasswordRejectsLeaveHashAndToken(t *testing.T) {
	svc := newSvc(nil)
	if _, err := svc.Register("a@example.edu.cn", "张三", "Abc12345"); err != nil {
		t.Fatal(err)
	}
	login, err := svc.Login("a@example.edu.cn", "Abc12345")
	if err != nil {
		t.Fatal(err)
	}
	before, _ := svc.store.FindByEmail("a@example.edu.cn")

	if err := svc.ChangePassword(login.AccessToken, "wrong-pass", "Abc123456"); err != ErrInvalidCredentials {
		t.Fatalf("wrong old password: %v", err)
	}
	if err := svc.ChangePassword(login.AccessToken, "Abc12345", "abc"); err != ErrWeakPassword {
		t.Fatalf("weak new password: %v", err)
	}
	after, _ := svc.store.FindByEmail("a@example.edu.cn")
	if after.PasswordHash != before.PasswordHash || after.TokensInvalidBefore != nil {
		t.Fatal("rejected change wrote the user")
	}
	if _, err := svc.Authenticate(login.AccessToken); err != nil {
		t.Fatal(err)
	}
}

func TestChangePasswordInvalidatesIssuedTokens(t *testing.T) {
	start := time.Date(2026, 10, 16, 8, 0, 0, 0, time.UTC)
	clk := clock.NewManual(start)
	svc := newSvc(clk)
	if _, err := svc.Register("a@example.edu.cn", "张三", "Abc12345"); err != nil {
		t.Fatal(err)
	}
	first, err := svc.Login("a@example.edu.cn", "Abc12345")
	if err != nil {
		t.Fatal(err)
	}
	clk.Set(start.Add(time.Second))
	if err := svc.ChangePassword(first.AccessToken, "Abc12345", "Abc123456"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Authenticate(first.AccessToken); err != ErrUnauthenticated {
		t.Fatalf("token issued before the change: %v", err)
	}
	if _, err := svc.Login("a@example.edu.cn", "Abc12345"); err != ErrInvalidCredentials {
		t.Fatal("old password still works")
	}
	next, err := svc.Login("a@example.edu.cn", "Abc123456")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Authenticate(next.AccessToken); err != nil {
		t.Fatal(err)
	}
}

func TestTokenExpiry(t *testing.T) {
	start := time.Date(2026, 10, 16, 8, 0, 0, 0, time.UTC)
	clk := clock.NewManual(start)
	svc := newSvc(clk)
	if _, err := svc.Register("a@example.edu.cn", "张三", "Abc12345"); err != nil {
		t.Fatal(err)
	}
	login, err := svc.Login("a@example.edu.cn", "Abc12345")
	if err != nil {
		t.Fatal(err)
	}
	if login.ExpiresIn != 7200 {
		t.Fatalf("expires_in %d", login.ExpiresIn)
	}
	clk.Set(start.Add(2*time.Hour - time.Second))
	if _, err := svc.Authenticate(login.AccessToken); err != nil {
		t.Fatal(err)
	}
	clk.Set(start.Add(2*time.Hour + time.Second))
	if _, err := svc.Authenticate(login.AccessToken); err != ErrUnauthenticated {
		t.Fatalf("expired token: %v", err)
	}
}
