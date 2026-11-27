package cryptobox

import (
	"bytes"
	"strings"
	"testing"
)

func TestSealOpenAndMask(t *testing.T) {
	key := bytes.Repeat([]byte{0x42}, 32)
	plain := []byte("sk-test-1234abcd")

	nonce, ct, err := Seal(key, plain)
	if err != nil {
		t.Fatalf("Seal failed: %v", err)
	}
	if bytes.Equal(ct, plain) || bytes.Contains(ct, plain) {
		t.Fatalf("ciphertext must not expose plaintext")
	}

	got, err := Open(key, nonce, ct)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatalf("Open got %q, want %q", got, plain)
	}

	wrongKey := bytes.Repeat([]byte{0x24}, 32)
	if _, err := Open(wrongKey, nonce, ct); err == nil {
		t.Fatalf("expected Open with wrong key to fail")
	}

	tampered := append([]byte(nil), ct...)
	tampered[0] ^= 0xff
	if _, err := Open(key, nonce, tampered); err == nil {
		t.Fatalf("expected Open with tampered ciphertext to fail")
	}

	if _, err := Open(key, []byte("short"), ct); err == nil {
		t.Fatalf("expected Open with bad nonce to fail")
	}

	if got := Mask(Last4(string(plain))); got != "****abcd" {
		t.Fatalf("Mask(Last4) = %q, want ****abcd", got)
	}
	if got := Last4("abc"); got != "abc" {
		t.Fatalf("Last4 short = %q, want abc", got)
	}
	if hex := SHA256Hex(plain); len(hex) != 64 || strings.Trim(hex, "0123456789abcdef") != "" {
		t.Fatalf("invalid SHA256Hex: %q", hex)
	}
}
