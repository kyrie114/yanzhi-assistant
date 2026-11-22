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
	"yanzhi/server/internal/graph"
	"yanzhi/server/internal/httpapi"
	"yanzhi/server/internal/identity"
	"yanzhi/server/internal/ingest"
	"yanzhi/server/internal/jobs"
	"yanzhi/server/internal/knowledge"
	"yanzhi/server/internal/platform/clock"
	pconfig "yanzhi/server/internal/platform/config"
	"yanzhi/server/internal/platform/db"
	"yanzhi/server/internal/platform/docreader"
	"yanzhi/server/internal/platform/files"
	"yanzhi/server/internal/provider"
	"yanzhi/server/internal/qa"
	"yanzhi/server/internal/retrieval"
)

const usage = "Usage: server --in-memory (or ACCOUNT_STORE=memory); JWT_SECRET must contain at least 32 bytes. Optional: HTTP_ADDR, BCRYPT_COST, FILES_ROOT, EMAIL_VERIFICATION_REQUIRED."

type config struct {
	addr          string
	jwtSecret     []byte
	bcryptCost    int
	filesRoot     string
	restrictEmail bool
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
	filesRoot := getenv("FILES_ROOT")
	restrictEmail := getenv("EMAIL_VERIFICATION_REQUIRED") == "true"
	return config{
		addr:          addr,
		jwtSecret:     []byte(secret),
		bcryptCost:    cost,
		filesRoot:     filesRoot,
		restrictEmail: restrictEmail,
	}, nil
}

func newHTTPServer(cfg config) *http.Server {
	return newHTTPServerWithContext(context.Background(), cfg)
}

func newHTTPServerWithContext(appCtx context.Context, cfg config) *http.Server {
	accStore := account.NewMemoryStore()
	svc := account.NewService(accStore, nil, cfg.jwtSecret, cfg.bcryptCost)
	var blobStore knowledge.BlobStore
	if cfg.filesRoot != "" {
		blobStore = files.Store{Root: cfg.filesRoot}
	} else {
		blobStore = knowledge.NewMemoryBlobStore()
	}
	kbStore := knowledge.NewMemoryStore(accStore, blobStore, nil)
	gw := provider.NewMemoryGateway()
	ret := retrieval.NewEngine(
		retrieval.NewMemoryCorpus(qa.MemoryChunkSource(kbStore)),
		gw,
		retrieval.DefaultSearchConfig(),
	)
	graphMem := graph.NewMemoryStore()
	qaStore := qa.NewMemoryStore(qa.WithAliveCheck(qa.MemoryAliveChecker(kbStore)))
	qaSvc := qa.NewService(appCtx, qaStore, kbStore, gw, ret, graphMem)

	handler := httpapi.NewHandler(svc,
		httpapi.WithKnowledge(kbStore),
		httpapi.WithQA(qaSvc),
		httpapi.WithEmailRestriction(cfg.restrictEmail),
	)
	return &http.Server{
		Addr:              cfg.addr,
		Handler:           handler,
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
	if os.Getenv("DATABASE_URL") != "" {
		if err := serveDatabase(); err != nil {
			log.Fatalf("server: %v", err)
		}
		return
	}
	cfg, err := loadConfig(os.Args[1:], os.Getenv)
	if errors.Is(err, flag.ErrHelp) {
		fmt.Fprintln(os.Stdout, usage)
		return
	}
	if err != nil {
		log.Fatalf("configuration: %v; %s", err, usage)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	srv := newHTTPServerWithContext(ctx, cfg)
	listener, err := net.Listen("tcp", cfg.addr)
	if err != nil {
		log.Fatalf("listen: %v", err)
	}
	log.Printf("account API listening on %s; memory storage: users and token revocations are lost on restart", listener.Addr())
	if err := serve(ctx, srv, listener); err != nil {
		log.Fatalf("server: %v", err)
	}
}

func serveDatabase() error {
	cfg, err := pconfig.Load()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := db.Migrate(ctx, cfg.DatabaseURL, cfg.MigrationsDir); err != nil {
		return err
	}
	pool, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := jobs.Recover(ctx, pool); err != nil {
		return err
	}
	accounts, err := identity.NewService(pool, cfg.JWTSecret, cfg.BcryptCost, clock.System{})
	if err != nil {
		return err
	}
	library := knowledge.Service{Pool: pool, Files: files.Store{Root: cfg.DataDir}}
	aiClient := provider.Client{
		HTTP: &http.Client{
			Transport: &http.Transport{
				ResponseHeaderTimeout: cfg.ModelTimeout,
			},
		},
	}
	models := provider.Store{Pool: pool, Key: cfg.AESKey, HTTP: aiClient}
	readerClient := docreader.New(cfg.DocreaderAddr, cfg.DocreaderToken, cfg.DocTimeout)
	runner := ingest.Runner{
		Pool:   pool,
		Files:  library.Files,
		Reader: readerClient,
		Models: models,
		AI:     aiClient,
		Chunk: ingest.Config{
			Min: cfg.ChunkMin, Max: cfg.ChunkMax, Overlap: cfg.ChunkOverlap,
			ParentMin: cfg.ParentMin, ParentMax: cfg.ParentMax,
		},
		EmbedBatch: cfg.EmbedBatch,
		Timeout:    cfg.DocTimeout,
	}
	gw := provider.NewPGGateway(pool, models, aiClient)
	ret := retrieval.NewEngine(retrieval.NewPGCorpus(pool), gw, retrieval.SearchConfig{
		TopK:             cfg.TopK,
		RecallLimit:      20,
		RRFK:             cfg.RRFK,
		BM25K1:           cfg.BM25K1,
		BM25B:            cfg.BM25B,
		DefaultThreshold: cfg.Threshold,
	})
	graphSvc := graph.Service{Pool: pool, AI: aiClient, Models: models, Timeout: cfg.DocTimeout}
	for range max(1, cfg.IngestWorkers) {
		go jobs.Loop(ctx, pool, []string{"ingest"}, runner.Run)
	}
	go jobs.Loop(ctx, pool, []string{"reembed"}, runner.Reembed)
	for range max(1, cfg.GraphWorkers) {
		go jobs.Loop(ctx, pool, []string{"graph_extract"}, graphSvc.Extract)
	}
	qaStore := qa.NewPGStore(pool)
	qaSvc := qa.NewService(ctx, qaStore, library, gw, ret, graphSvc)

	listener, err := net.Listen("tcp", cfg.HTTPAddr)
	if err != nil {
		return err
	}
	log.Printf("library API listening on %s", listener.Addr())
	return serve(ctx, &http.Server{
		Handler: httpapi.NewLibraryHandler(
			accounts,
			library,
			models,
			httpapi.WithQA(qaSvc),
			httpapi.WithGraph(graphSvc),
			httpapi.WithHealthProbe(readerClient.Healthy),
			httpapi.WithEmailRestriction(cfg.EmailRestrict),
		),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}, listener)
}
