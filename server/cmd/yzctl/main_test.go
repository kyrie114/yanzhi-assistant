package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) {
	return 0, errors.New("boom")
}

func TestParseArgsValid(t *testing.T) {
	tests := []struct {
		name  string
		args  []string
		stdin io.Reader
		want  command
	}{
		{
			name: "create-admin with explicit password",
			args: []string{"create-admin", "--email", " admin@example.com ", "--name", " 管理员 ", "--password", "Admin12345"},
			want: command{action: actionCreateAdmin, email: "admin@example.com", name: "管理员", password: "Admin12345"},
		},
		{
			name:  "create-admin reads lf line from stdin when password omitted",
			args:  []string{"create-admin", "--email", "admin@example.com", "--name", "管理员"},
			stdin: strings.NewReader("Admin12345\nignored-second-line\n"),
			want:  command{action: actionCreateAdmin, email: "admin@example.com", name: "管理员", password: "Admin12345"},
		},
		{
			name:  "create-admin reads crlf line from stdin when password empty",
			args:  []string{"create-admin", "--email", "admin@example.com", "--name", "管理员", "--password", ""},
			stdin: strings.NewReader("Admin12345\r\n"),
			want:  command{action: actionCreateAdmin, email: "admin@example.com", name: "管理员", password: "Admin12345"},
		},
		{
			name:  "create-admin reads eof-terminated stdin without newline",
			args:  []string{"create-admin", "--email", "admin@example.com", "--name", "管理员"},
			stdin: strings.NewReader("Admin12345"),
			want:  command{action: actionCreateAdmin, email: "admin@example.com", name: "管理员", password: "Admin12345"},
		},
		{
			name: "reset-password with explicit password",
			args: []string{"reset-password", "--email", "user@example.com", "--password", "NextPass123"},
			want: command{action: actionResetPassword, email: "user@example.com", password: "NextPass123"},
		},
		{
			name:  "reset-password reads stdin with crlf trimmed",
			args:  []string{"reset-password", "--email", "user@example.com"},
			stdin: strings.NewReader("NextPass123\r\n"),
			want:  command{action: actionResetPassword, email: "user@example.com", password: "NextPass123"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseArgs(tc.args, tc.stdin)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestParseArgsInvalid(t *testing.T) {
	tests := []struct {
		name  string
		args  []string
		stdin io.Reader
	}{
		{name: "no arguments"},
		{name: "unknown subcommand", args: []string{"delete-admin"}},
		{name: "create-admin help", args: []string{"create-admin", "--help"}},
		{name: "create-admin unknown flag", args: []string{"create-admin", "--unknown", "x"}},
		{name: "create-admin positional arg", args: []string{"create-admin", "--email", "a@b.com", "--name", "A", "extra"}},
		{name: "create-admin missing email", args: []string{"create-admin", "--name", "管理员", "--password", "Admin12345"}},
		{name: "create-admin blank name", args: []string{"create-admin", "--email", "a@b.com", "--name", "   ", "--password", "Admin12345"}},
		{name: "create-admin empty stdin", args: []string{"create-admin", "--email", "a@b.com", "--name", "管理员"}, stdin: strings.NewReader("")},
		{name: "create-admin newline-only stdin", args: []string{"create-admin", "--email", "a@b.com", "--name", "管理员"}, stdin: strings.NewReader("\r\n")},
		{name: "create-admin nil stdin", args: []string{"create-admin", "--email", "a@b.com", "--name", "管理员"}},
		{name: "create-admin stdin read error", args: []string{"create-admin", "--email", "a@b.com", "--name", "管理员"}, stdin: failingReader{}},
		{name: "reset-password missing email", args: []string{"reset-password", "--password", "NextPass123"}},
		{name: "reset-password rejects name flag", args: []string{"reset-password", "--email", "a@b.com", "--name", "X", "--password", "NextPass123"}},
		{name: "reset-password positional arg", args: []string{"reset-password", "--email", "a@b.com", "--password", "NextPass123", "extra"}},
		{name: "reset-password empty stdin", args: []string{"reset-password", "--email", "a@b.com"}, stdin: strings.NewReader("\n")},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got, err := parseArgs(tc.args, tc.stdin); err == nil {
				t.Fatalf("expected error, got %+v", got)
			}
		})
	}
}

func TestLoadConfig(t *testing.T) {
	t.Run("requires DATABASE_URL", func(t *testing.T) {
		if _, err := loadConfig(func(string) string { return "" }); err == nil {
			t.Fatal("expected error when DATABASE_URL is missing")
		}
	})

	t.Run("defaults bcrypt cost", func(t *testing.T) {
		cfg, err := loadConfig(func(k string) string {
			if k == "DATABASE_URL" {
				return "postgres://u:p@localhost:5432/yanzhi"
			}
			return ""
		})
		if err != nil {
			t.Fatal(err)
		}
		if cfg.bcryptCost != bcrypt.DefaultCost {
			t.Fatalf("bcryptCost = %d, want %d", cfg.bcryptCost, bcrypt.DefaultCost)
		}
	})

	for _, cost := range []string{"4", "12", "31"} {
		t.Run("accepts bcrypt cost "+cost, func(t *testing.T) {
			cfg, err := loadConfig(func(k string) string {
				switch k {
				case "DATABASE_URL":
					return "postgres://u:p@localhost:5432/yanzhi"
				case "BCRYPT_COST":
					return cost
				default:
					return ""
				}
			})
			if err != nil {
				t.Fatal(err)
			}
			if cfg.databaseURL == "" || cfg.bcryptCost < bcrypt.MinCost || cfg.bcryptCost > bcrypt.MaxCost {
				t.Fatalf("unexpected cfg: %+v", cfg)
			}
		})
	}

	for _, bad := range []string{"3", "32", "fast", "10.5"} {
		t.Run("rejects bcrypt cost "+bad, func(t *testing.T) {
			_, err := loadConfig(func(k string) string {
				switch k {
				case "DATABASE_URL":
					return "postgres://u:p@localhost:5432/yanzhi"
				case "BCRYPT_COST":
					return bad
				default:
					return ""
				}
			})
			if err == nil {
				t.Fatalf("expected error for BCRYPT_COST=%q", bad)
			}
		})
	}
}

func TestRunRejectsInvalidInputAndConfigBeforeDB(t *testing.T) {
	var out bytes.Buffer
	if err := run(context.Background(), nil, strings.NewReader(""), &out, func(string) string { return "" }); err == nil {
		t.Fatal("expected usage error")
	}
	if err := run(context.Background(), []string{"create-admin", "--email", "a@b.com", "--name", "管理员", "--password", "Admin12345"}, nil, &out, func(string) string { return "" }); err == nil {
		t.Fatal("expected missing DATABASE_URL error")
	}
	if out.Len() != 0 {
		t.Fatalf("expected no stdout on failure, got %q", out.String())
	}
}
