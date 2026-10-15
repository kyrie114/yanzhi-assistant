package identity

import (
	"testing"
	"time"
)

func TestPasswordRule(t *testing.T) {
	if PasswordOK("abc123") || PasswordOK("abcdefgh") || !PasswordOK("Abc12345") {
		t.Fatal("password rule")
	}
}

func TestTokenInvalidBefore(t *testing.T) {
	now := time.Date(2026, 10, 14, 8, 0, 0, 0, time.UTC)
	iat := now.Add(-time.Hour)
	exp := now.Add(time.Hour)
	cut := now
	if TokenActive(iat, exp, now, &cut, false) {
		t.Fatal("issued before password change")
	}
	if !TokenActive(now.Add(time.Second), exp, now, &cut, false) {
		t.Fatal("new token")
	}
	if TokenActive(now.Add(time.Second), exp, now, &cut, true) {
		t.Fatal("revoked")
	}
}
