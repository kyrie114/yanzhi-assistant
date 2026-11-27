package provider

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"yanzhi/server/internal/apperr"
	"yanzhi/server/internal/platform/db"
)

var defaultRetryDelays = []time.Duration{1 * time.Second, 2 * time.Second}

// CallMeta 是写入 model_call_logs 的脱敏元信息，绝不包含提示词全文、回答内容或明文密钥（NFR-07）。
type CallMeta struct {
	RequestID  string
	UserID     string
	Kind       string // "chat" | "embedding" | "rerank"
	Model      string
	ElapsedMS  int
	Usage      Usage
	Status     string // "ok" | "skipped" | "error" | "cancelled"
	PromptHash string
}

// Gateway 统一屏蔽模型配置读取、失败重试与调用摘要落库策略；自身保持领域无关，不拼装上游 Prompt。
type Gateway interface {
	RequireModel(ctx context.Context, kind string) (Remote, error)
	ActiveGeneration(ctx context.Context) (int64, error)
	Embed(ctx context.Context, meta CallMeta, inputs []string) ([][]float32, error)
	Rerank(ctx context.Context, meta CallMeta, query string, docs []string) ([]int, string)
	Complete(ctx context.Context, meta CallMeta, system, user string) (string, error)
	Stream(ctx context.Context, meta CallMeta, system, user string, onDelta func(string) error) error
}

// PromptSHA256 为提示词计算十六进制摘要，确保日志侧仅保留指纹而不触碰正文。
func PromptSHA256(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		_, _ = h.Write([]byte(p))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// PGGateway 基于 PostgreSQL Store 与 HTTP Client 实现 Gateway。
type PGGateway struct {
	Pool        *pgxpool.Pool
	Store       Store
	Client      Client
	Logger      *slog.Logger
	RetryDelays []time.Duration
}

func NewPGGateway(pool *pgxpool.Pool, store Store, client Client) *PGGateway {
	return &PGGateway{
		Pool:        pool,
		Store:       store,
		Client:      client,
		RetryDelays: slices.Clone(defaultRetryDelays),
	}
}

func (g *PGGateway) RequireModel(ctx context.Context, kind string) (Remote, error) {
	return g.Store.Load(ctx, kind)
}

func (g *PGGateway) ActiveGeneration(ctx context.Context) (int64, error) {
	return g.Store.ActiveGeneration(ctx)
}

func (g *PGGateway) Embed(ctx context.Context, meta CallMeta, inputs []string) ([][]float32, error) {
	remote, err := g.Store.Load(ctx, "embedding")
	meta.Kind = "embedding"
	if err != nil {
		meta.Status = "error"
		g.recordLog(ctx, meta)
		return nil, err
	}
	meta.Model = remote.Model
	start := time.Now()
	var vecs [][]float32
	var usage Usage
	err = g.withRetries(ctx, func() error {
		var callErr error
		vecs, usage, callErr = g.Client.Embed(ctx, remote, inputs)
		return callErr
	})
	meta.ElapsedMS = int(time.Since(start).Milliseconds())
	meta.Usage = usage
	if err != nil {
		meta.Status = errorStatus(ctx, err)
		g.recordLog(ctx, meta)
		return nil, err
	}
	meta.Status = "ok"
	g.recordLog(ctx, meta)
	return vecs, nil
}

func (g *PGGateway) Rerank(ctx context.Context, meta CallMeta, query string, docs []string) ([]int, string) {
	meta.Kind = "rerank"
	if len(docs) == 0 {
		meta.Status = "skipped"
		g.recordLog(ctx, meta)
		return nil, "skipped"
	}
	remote, err := g.Store.Load(ctx, "rerank")
	if err != nil {
		meta.Status = "skipped"
		g.recordLog(ctx, meta)
		return nil, "skipped"
	}
	meta.Model = remote.Model
	start := time.Now()
	var results []RerankResult
	var usage Usage
	err = g.withRetries(ctx, func() error {
		var callErr error
		results, usage, callErr = g.Client.Rerank(ctx, remote, query, docs)
		return callErr
	})
	meta.ElapsedMS = int(time.Since(start).Milliseconds())
	meta.Usage = usage
	if err != nil || len(results) == 0 {
		meta.Status = "error"
		g.recordLog(ctx, meta)
		return nil, "error"
	}
	meta.Status = "ok"
	g.recordLog(ctx, meta)
	indices := make([]int, 0, len(results))
	for _, r := range results {
		indices = append(indices, r.Index)
	}
	return indices, "ok"
}

func (g *PGGateway) Complete(ctx context.Context, meta CallMeta, system, user string) (string, error) {
	remote, err := g.Store.Load(ctx, "chat")
	meta.Kind = "chat"
	meta.PromptHash = PromptSHA256(system, "\n", user)
	if err != nil {
		meta.Status = "error"
		g.recordLog(ctx, meta)
		return "", err
	}
	meta.Model = remote.Model
	start := time.Now()
	var out string
	var usage Usage
	err = g.withRetries(ctx, func() error {
		var callErr error
		out, usage, callErr = g.Client.Complete(ctx, remote, system, user)
		return callErr
	})
	meta.ElapsedMS = int(time.Since(start).Milliseconds())
	meta.Usage = usage
	if err != nil {
		meta.Status = errorStatus(ctx, err)
		g.recordLog(ctx, meta)
		return "", err
	}
	meta.Status = "ok"
	g.recordLog(ctx, meta)
	return out, nil
}

func (g *PGGateway) Stream(ctx context.Context, meta CallMeta, system, user string, onDelta func(string) error) error {
	remote, err := g.Store.Load(ctx, "chat")
	meta.Kind = "chat"
	meta.PromptHash = PromptSHA256(system, "\n", user)
	if err != nil {
		meta.Status = "error"
		g.recordLog(ctx, meta)
		return err
	}
	meta.Model = remote.Model
	start := time.Now()
	var emitted bool
	var usage Usage
	err = g.withRetries(ctx, func() error {
		if emitted {
			// 已向客户端输出增量后禁止重试，避免产生重复片段。
			return errNoRetry
		}
		var callErr error
		usage, callErr = g.Client.Stream(ctx, remote, system, user, func(delta string) error {
			if delta != "" {
				emitted = true
			}
			return onDelta(delta)
		})
		return callErr
	})
	meta.ElapsedMS = int(time.Since(start).Milliseconds())
	meta.Usage = usage
	if err != nil {
		meta.Status = errorStatus(ctx, err)
		g.recordLog(ctx, meta)
		return err
	}
	meta.Status = "ok"
	g.recordLog(ctx, meta)
	return nil
}

var errNoRetry = errors.New("no retry after stream delta")

func (g *PGGateway) withRetries(ctx context.Context, fn func() error) error {
	delays := g.RetryDelays
	if delays == nil {
		delays = defaultRetryDelays
	}
	var lastErr error
	for attempt := 0; attempt <= len(delays); attempt++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		err := fn()
		if err == nil {
			return nil
		}
		if errors.Is(err, errNoRetry) {
			return lastErr
		}
		lastErr = err
		if errors.Is(err, context.Canceled) || ctx.Err() != nil || attempt == len(delays) {
			return err
		}
		timer := time.NewTimer(delays[attempt])
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return lastErr
}

func errorStatus(ctx context.Context, err error) string {
	if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
		return "cancelled"
	}
	return "error"
}

func (g *PGGateway) recordLog(ctx context.Context, meta CallMeta) {
	RecordModelCall(ctx, g.Pool, g.Logger, meta)
}

// RecordModelCall 将脱敏模型调用元数据写入 model_call_logs 并可选输出结构化 JSON 日志（NFR-07）。
func RecordModelCall(ctx context.Context, pool *pgxpool.Pool, logger *slog.Logger, meta CallMeta) {
	if logger != nil {
		logger.Info("model_call",
			"request_id", meta.RequestID,
			"user_id", meta.UserID,
			"kind", meta.Kind,
			"model", meta.Model,
			"elapsed_ms", meta.ElapsedMS,
			"prompt_tokens", meta.Usage.Prompt,
			"completion_tokens", meta.Usage.Completion,
			"status", meta.Status,
		)
	}
	if pool == nil {
		return
	}
	reqUUID, ok := db.UUID(meta.RequestID)
	if !ok {
		return
	}
	var userVal any
	if u, ok := db.UUID(meta.UserID); ok {
		userVal = u
	}
	var hashVal any
	if meta.PromptHash != "" {
		hashVal = meta.PromptHash
	}
	logCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	_, _ = pool.Exec(logCtx, `
		INSERT INTO model_call_logs
			(id, request_id, user_id, kind, model, elapsed_ms, prompt_tokens, completion_tokens, status, prompt_sha256, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
		db.MustUUID(db.NewID()), reqUUID, userVal, meta.Kind, meta.Model,
		meta.ElapsedMS, meta.Usage.Prompt, meta.Usage.Completion, meta.Status, hashVal, time.Now().UTC())
}

// MemoryGateway 是用于 --in-memory 模式与 CI 无数据库单元测试的可编程网关替身。
type MemoryGateway struct {
	mu         sync.Mutex
	generation int64
	models     map[string]Remote
	calls      []CallMeta

	EmbedFn    func(ctx context.Context, inputs []string) ([][]float32, error)
	RerankFn   func(ctx context.Context, query string, docs []string) ([]int, error)
	CompleteFn func(ctx context.Context, system, user string) (string, error)
	StreamFn   func(ctx context.Context, system, user string, onDelta func(string) error) error
}

// NewMemoryGateway 创建默认已配置 chat 与 embedding 槽位的内存模型网关。
func NewMemoryGateway() *MemoryGateway {
	return &MemoryGateway{
		generation: 1,
		models: map[string]Remote{
			"chat":      {Kind: "chat", BaseURL: "memory://chat", Model: "mock-chat"},
			"embedding": {Kind: "embedding", BaseURL: "memory://embed", Model: "mock-embed", Dimension: 4},
		},
	}
}

func (m *MemoryGateway) SetModel(kind string, remote Remote) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.models == nil {
		m.models = make(map[string]Remote)
	}
	remote.Kind = kind
	m.models[kind] = remote
}

func (m *MemoryGateway) RemoveModel(kind string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.models, kind)
}

func (m *MemoryGateway) SetGeneration(gen int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.generation = gen
}

func (m *MemoryGateway) Calls() []CallMeta {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.calls)
}

func (m *MemoryGateway) ModelMapSnapshot() map[string]Remote {
	m.mu.Lock()
	defer m.mu.Unlock()
	return maps.Clone(m.models)
}

func (m *MemoryGateway) record(meta CallMeta) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, meta)
}

func (m *MemoryGateway) RequireModel(ctx context.Context, kind string) (Remote, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.models[kind]
	if !ok {
		return Remote{}, apperr.ErrModelMissing
	}
	return r, nil
}

func (m *MemoryGateway) ActiveGeneration(ctx context.Context) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.generation <= 0 {
		return 1, nil
	}
	return m.generation, nil
}

func (m *MemoryGateway) Embed(ctx context.Context, meta CallMeta, inputs []string) ([][]float32, error) {
	meta.Kind = "embedding"
	remote, err := m.RequireModel(ctx, "embedding")
	if err != nil {
		meta.Status = "error"
		m.record(meta)
		return nil, err
	}
	meta.Model = remote.Model
	if m.EmbedFn == nil {
		meta.Status = "error"
		m.record(meta)
		return nil, apperr.ErrModelDown
	}
	vecs, err := m.EmbedFn(ctx, inputs)
	if err != nil {
		meta.Status = errorStatus(ctx, err)
		m.record(meta)
		return nil, err
	}
	meta.Status = "ok"
	m.record(meta)
	return vecs, nil
}

func (m *MemoryGateway) Rerank(ctx context.Context, meta CallMeta, query string, docs []string) ([]int, string) {
	meta.Kind = "rerank"
	if len(docs) == 0 {
		meta.Status = "skipped"
		m.record(meta)
		return nil, "skipped"
	}
	remote, err := m.RequireModel(ctx, "rerank")
	if err != nil || m.RerankFn == nil {
		meta.Status = "skipped"
		m.record(meta)
		return nil, "skipped"
	}
	meta.Model = remote.Model
	indices, err := m.RerankFn(ctx, query, docs)
	if err != nil || len(indices) == 0 {
		meta.Status = "error"
		m.record(meta)
		return nil, "error"
	}
	meta.Status = "ok"
	m.record(meta)
	return indices, "ok"
}

func (m *MemoryGateway) Complete(ctx context.Context, meta CallMeta, system, user string) (string, error) {
	meta.Kind = "chat"
	meta.PromptHash = PromptSHA256(system, "\n", user)
	remote, err := m.RequireModel(ctx, "chat")
	if err != nil {
		meta.Status = "error"
		m.record(meta)
		return "", err
	}
	meta.Model = remote.Model
	if m.CompleteFn == nil {
		meta.Status = "error"
		m.record(meta)
		return "", apperr.ErrModelDown
	}
	out, err := m.CompleteFn(ctx, system, user)
	if err != nil {
		meta.Status = errorStatus(ctx, err)
		m.record(meta)
		return "", err
	}
	meta.Status = "ok"
	m.record(meta)
	return out, nil
}

func (m *MemoryGateway) Stream(ctx context.Context, meta CallMeta, system, user string, onDelta func(string) error) error {
	meta.Kind = "chat"
	meta.PromptHash = PromptSHA256(system, "\n", user)
	remote, err := m.RequireModel(ctx, "chat")
	if err != nil {
		meta.Status = "error"
		m.record(meta)
		return err
	}
	meta.Model = remote.Model
	if m.StreamFn == nil {
		meta.Status = "error"
		m.record(meta)
		return apperr.ErrModelDown
	}
	if err := m.StreamFn(ctx, system, user, onDelta); err != nil {
		meta.Status = errorStatus(ctx, err)
		m.record(meta)
		return err
	}
	meta.Status = "ok"
	m.record(meta)
	return nil
}
