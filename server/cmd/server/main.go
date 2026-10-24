// Command server 是研智助手主服务的组合根：连库、装配服务、挂路由、跑 worker。
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"yanzhi/server/internal/graph"
	"yanzhi/server/internal/httpapi"
	"yanzhi/server/internal/identity"
	"yanzhi/server/internal/ingest"
	"yanzhi/server/internal/jobs"
	"yanzhi/server/internal/knowledge"
	"yanzhi/server/internal/platform/clock"
	"yanzhi/server/internal/platform/config"
	"yanzhi/server/internal/platform/db"
	"yanzhi/server/internal/platform/docreader"
	"yanzhi/server/internal/platform/files"
	"yanzhi/server/internal/provider"
	"yanzhi/server/internal/qa"
	"yanzhi/server/internal/suggest"
)

func main() {
	if err := run(); err != nil {
		slog.Error("server stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	if err := db.Migrate(ctx, cfg.DatabaseURL, cfg.MigrationsDir); err != nil {
		return err
	}
	// 上次进程留下的 parsing/embedding 标成失败，不自动重跑，也不计入手动重试次数。
	if err := jobs.Recover(ctx, pool); err != nil {
		return err
	}

	clk := clock.System{}
	ids, err := identity.NewService(pool, cfg.JWTSecret, cfg.BcryptCost, clk)
	if err != nil {
		return err
	}
	fileStore := files.Store{Root: cfg.DataDir}
	ai := provider.Client{}
	models := provider.Store{Pool: pool, Key: cfg.AESKey, HTTP: ai}
	kb := knowledge.Service{Pool: pool, Files: fileStore}
	graphSvc := graph.Service{Pool: pool, AI: ai, Models: models, Timeout: cfg.ModelTimeout}
	qaSvc := &qa.Service{
		Pool: pool, AI: ai, Models: models, Graph: graphSvc,
		TopK: cfg.TopK, RRFK: cfg.RRFK, K1: cfg.BM25K1, B: cfg.BM25B,
		Threshold: cfg.Threshold, Stops: qa.NewStops(), Timeout: cfg.ModelTimeout,
	}
	sug := suggest.NewMemory()

	app := &httpapi.App{
		Identity: ids, Knowledge: kb, QA: qaSvc, Graph: graphSvc,
		Models: models, Suggest: sug, RestrictEmail: cfg.EmailRestrict,
	}

	runner := ingest.Runner{
		Pool: pool, Files: fileStore,
		Reader: docreader.New(cfg.DocreaderAddr, cfg.DocreaderToken, cfg.DocTimeout),
		Models: models, AI: ai,
		Chunk: ingest.Config{
			Min: cfg.ChunkMin, Max: cfg.ChunkMax, Overlap: cfg.ChunkOverlap,
			ParentMin: cfg.ParentMin, ParentMax: cfg.ParentMax,
		},
		EmbedBatch: cfg.EmbedBatch, Timeout: cfg.ModelTimeout,
	}

	var workers sync.WaitGroup
	startIngest := func() {
		defer workers.Done()
		jobs.Loop(ctx, pool, []string{"ingest", "reembed"}, func(c context.Context, j jobs.Job) error {
			if j.Kind == "reembed" {
				return runner.Reembed(c, j)
			}
			return runner.Run(c, j)
		})
	}
	for i := 0; i < cfg.IngestWorkers; i++ {
		workers.Add(1)
		go startIngest()
	}
	startGraph := func() {
		defer workers.Done()
		jobs.Loop(ctx, pool, []string{"graph_extract"}, graphSvc.Extract)
	}
	for i := 0; i < cfg.GraphWorkers; i++ {
		workers.Add(1)
		go startGraph()
	}

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           app.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       5 * time.Minute,
		WriteTimeout:      0, // SSE 由调用方控制结束，不能设总写入超时
		IdleTimeout:       2 * time.Minute,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	slog.Info("listening", "addr", cfg.HTTPAddr, "ingest_workers", cfg.IngestWorkers, "graph_workers", cfg.GraphWorkers)
	serveErr := srv.ListenAndServe()
	if errors.Is(serveErr, http.ErrServerClosed) {
		serveErr = nil
	}
	stop()
	workers.Wait()
	return serveErr
}
