// Command server runs the FR-01 account API with an explicitly selected memory store.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/crypto/bcrypt"

	"yanzhi/server/internal/account"
	"yanzhi/server/internal/httpapi"
)

const usage = "Usage: server --in-memory (or ACCOUNT_STORE=memory); JWT_SECRET must contain at least 32 bytes. Optional: HTTP_ADDR, BCRYPT_COST."

type config struct {
	addr       string
	jwtSecret  []byte
	bcryptCost int
}

// loadConfig is separate from startup so invalid settings cannot start a server.
func loadConfig(args []string, getenv func(string) string) (config, error) {
	flags := flag.NewFlagSet("server", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	inMemory := flags.Bool("in-memory", false, "explicitly enable nonpersistent account storage")
	if err := flags.Parse(args); err != nil {
		return config{}, err
	}
	if flags.NArg() != 0 {
		return config{}, fmt.Errorf("unexpected positional arguments")
	}
	store := getenv("ACCOUNT_STORE")
	if store != "" && store != "memory" {
		return config{}, fmt.Errorf("ACCOUNT_STORE must be memory; other stores are not implemented")
	}
	if !*inMemory && store != "memory" {
		return config{}, fmt.Errorf("select nonpersistent storage explicitly with --in-memory or ACCOUNT_STORE=memory")
	}
	secret := getenv("JWT_SECRET")
	if len(secret) < 32 || strings.TrimSpace(secret) == "" {
		return config{}, fmt.Errorf("JWT_SECRET must contain at least 32 bytes and cannot be blank")
	}
	addr := getenv("HTTP_ADDR")
	if addr == "" {
		addr = "127.0.0.1:8080"
	}
	_, portText, err := net.SplitHostPort(addr)
	if err != nil {
		return config{}, fmt.Errorf("HTTP_ADDR must be a host:port address: %w", err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 0 || port > 65535 {
		return config{}, fmt.Errorf("HTTP_ADDR port must be an integer between 0 and 65535")
	}
	cost := bcrypt.DefaultCost
	if raw := getenv("BCRYPT_COST"); raw != "" {
		cost, err = strconv.Atoi(raw)
		if err != nil || cost < bcrypt.MinCost || cost > bcrypt.MaxCost {
			return config{}, fmt.Errorf("BCRYPT_COST must be an integer between %d and %d", bcrypt.MinCost, bcrypt.MaxCost)
		}
	}
	return config{addr: addr, jwtSecret: []byte(secret), bcryptCost: cost}, nil
}

func newHTTPServer(cfg config) *http.Server {
	svc := account.NewService(account.NewMemoryStore(), nil, cfg.jwtSecret, cfg.bcryptCost)
	return &http.Server{
		Addr:              cfg.addr,
		Handler:           httpapi.NewHandler(svc),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
}

// serve uses the same graceful shutdown path for process signals and tests.
func serve(ctx context.Context, srv *http.Server, listener net.Listener) error {
	done := make(chan error, 1)
	go func() { done <- srv.Serve(listener) }()
	select {
	case err := <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			_ = srv.Close()
			<-done
			return fmt.Errorf("shutdown: %w", err)
		}
		err := <-done
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	}
}

func main() {
	cfg, err := loadConfig(os.Args[1:], os.Getenv)
	if errors.Is(err, flag.ErrHelp) {
		fmt.Fprintln(os.Stdout, usage)
		return
	}
	if err != nil {
		log.Fatalf("configuration: %v; %s", err, usage)
	}
	srv := newHTTPServer(cfg)
	listener, err := net.Listen("tcp", cfg.addr)
	if err != nil {
		log.Fatalf("listen: %v", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	log.Printf("account API listening on %s; memory storage: users and token revocations are lost on restart", listener.Addr())
	if err := serve(ctx, srv, listener); err != nil {
		log.Fatalf("server: %v", err)
	}
}
