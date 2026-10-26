package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

const testSecret = "account-entrypoint-test-secret-32-bytes"

func TestLoadConfigRequiresExplicitMemoryAndValidSettings(t *testing.T) {
	tests := []struct {
		name  string
		args  []string
		key   string
		value string
	}{
		{name: "memory store must be explicit", key: "ACCOUNT_STORE", value: ""},
		{name: "missing secret", key: "JWT_SECRET", value: ""},
		{name: "short secret", key: "JWT_SECRET", value: "short"},
		{name: "blank secret", key: "JWT_SECRET", value: strings.Repeat(" ", 32)},
		{name: "unsupported store", args: []string{"--in-memory"}, key: "ACCOUNT_STORE", value: "postgres"},
		{name: "nonnumeric bcrypt cost", key: "BCRYPT_COST", value: "fast"},
		{name: "bcrypt below minimum", key: "BCRYPT_COST", value: "3"},
		{name: "bcrypt above maximum", key: "BCRYPT_COST", value: "32"},
		{name: "missing port", key: "HTTP_ADDR", value: "127.0.0.1"},
		{name: "invalid port", key: "HTTP_ADDR", value: "127.0.0.1:65536"},
		{name: "unexpected argument", args: []string{"extra"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := map[string]string{"ACCOUNT_STORE": "memory", "JWT_SECRET": testSecret}
			if tt.key != "" {
				env[tt.key] = tt.value
			}
			if _, err := loadConfig(tt.args, func(k string) string { return env[k] }); err == nil {
				t.Fatal("invalid configuration accepted")
			}
		})
	}
}

func TestLoadConfigDefaultsAndExplicitFlag(t *testing.T) {
	for _, args := range [][]string{nil, {"--in-memory"}} {
		env := map[string]string{"JWT_SECRET": testSecret}
		if len(args) == 0 {
			env["ACCOUNT_STORE"] = "memory"
		}
		cfg, err := loadConfig(args, func(k string) string { return env[k] })
		if err != nil {
			t.Fatal(err)
		}
		if cfg.addr != "127.0.0.1:8080" || cfg.bcryptCost != 10 || string(cfg.jwtSecret) != testSecret {
			t.Fatalf("unexpected defaults: addr=%s bcrypt=%d", cfg.addr, cfg.bcryptCost)
		}
	}
}

// This test crosses a real TCP listener, the runnable server, routes and account service.
func TestAccountServerLifecycle(t *testing.T) {
	env := map[string]string{"ACCOUNT_STORE": "memory", "JWT_SECRET": testSecret, "HTTP_ADDR": "127.0.0.1:0", "BCRYPT_COST": "4"}
	cfg, err := loadConfig(nil, func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	srv := newHTTPServer(cfg)
	listener, err := net.Listen("tcp", cfg.addr)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- serve(ctx, srv, listener) }()
	var stopOnce sync.Once
	stop := func() {
		stopOnce.Do(func() {
			cancel()
			select {
			case err := <-done:
				if err != nil {
					t.Errorf("shutdown: %v", err)
				}
			case <-time.After(7 * time.Second):
				_ = srv.Close()
				t.Error("server did not shut down")
			}
		})
	}
	t.Cleanup(stop)
	client := &http.Client{Timeout: 3 * time.Second}
	t.Cleanup(client.CloseIdleConnections)
	baseURL := "http://" + listener.Addr().String()
	request := func(method, path, token string, body any, wantStatus int) []byte {
		t.Helper()
		var payload bytes.Buffer
		if body != nil {
			if err := json.NewEncoder(&payload).Encode(body); err != nil {
				t.Fatal(err)
			}
		}
		req, err := http.NewRequest(method, baseURL+path, &payload)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != wantStatus {
			t.Fatalf("%s %s: status=%d want=%d body=%s", method, path, resp.StatusCode, wantStatus, data)
		}
		return data
	}
	login := func(password string) string {
		t.Helper()
		body := request(http.MethodPost, "/api/v1/auth/login", "", map[string]string{"email": "student@example.edu.cn", "password": password}, http.StatusOK)
		var result struct {
			AccessToken string `json:"access_token"`
		}
		if err := json.Unmarshal(body, &result); err != nil || result.AccessToken == "" {
			t.Fatalf("missing login token: %s (decode=%v)", body, err)
		}
		return result.AccessToken
	}
	request(http.MethodPost, "/api/v1/auth/register", "", map[string]string{"email": "student@example.edu.cn", "name": "学生", "password": "Original123"}, http.StatusCreated)
	first := login("Original123")
	second := login("Original123")
	request(http.MethodGet, "/api/v1/me", first, nil, http.StatusOK)
	request(http.MethodPost, "/api/v1/auth/logout", first, nil, http.StatusNoContent)
	request(http.MethodGet, "/api/v1/me", first, nil, http.StatusUnauthorized)
	request(http.MethodGet, "/api/v1/me", second, nil, http.StatusOK)
	third := login("Original123")
	request(http.MethodPost, "/api/v1/auth/password", second, map[string]string{"old_password": "Original123", "new_password": "Replacement456"}, http.StatusOK)
	request(http.MethodGet, "/api/v1/me", second, nil, http.StatusUnauthorized)
	request(http.MethodGet, "/api/v1/me", third, nil, http.StatusUnauthorized)
	request(http.MethodPost, "/api/v1/auth/login", "", map[string]string{"email": "student@example.edu.cn", "password": "Original123"}, http.StatusUnauthorized)
	fresh := login("Replacement456")
	request(http.MethodGet, "/api/v1/me", fresh, nil, http.StatusOK)
	stop()
	if resp, err := client.Get(baseURL + "/api/v1/me"); err == nil {
		resp.Body.Close()
		t.Fatal("server still accepts requests after shutdown")
	}
}

func TestShutdownFinishesActiveRequest(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	finish := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(finish)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release
		w.WriteHeader(http.StatusNoContent)
	})}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	t.Cleanup(func() { _ = srv.Close() })
	done := make(chan error, 1)
	go func() { done <- serve(ctx, srv, listener) }()
	client := &http.Client{Timeout: 3 * time.Second}
	t.Cleanup(client.CloseIdleConnections)
	requestDone := make(chan error, 1)
	go func() {
		resp, err := client.Get("http://" + listener.Addr().String())
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode != http.StatusNoContent {
				err = io.ErrUnexpectedEOF
			}
		}
		requestDone <- err
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("request did not reach server")
	}
	cancel()
	select {
	case err := <-done:
		t.Fatalf("server stopped before active request finished: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	finish()
	if err := <-requestDone; err != nil {
		t.Fatalf("active request interrupted: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(7 * time.Second):
		t.Fatal("server did not finish graceful shutdown")
	}
}
