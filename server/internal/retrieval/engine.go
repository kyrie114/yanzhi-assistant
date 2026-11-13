package retrieval

import (
	"context"
	"slices"
	"strings"
	"sync"

	"yanzhi/server/internal/provider"
	"yanzhi/server/internal/textutil"
)

// Scope 是由 knowledge.ResolvedScope 满足的最小只读检索范围约束。
type Scope interface {
	OwnerID() string
	DocIDs() []string
	ThresholdOr(fallback float64) float64
}

// Hit 表示检索或图谱补充返回的命中切片及所属父块上下文。
type Hit struct {
	ChunkID    string
	DocumentID string
	Title      string
	PageStart  int
	Text       string
	ParentText string // 仅作模型生成上下文，不单独分配引用别名，也不改变 PageStart
}

// SearchConfig 定义混合检索与重排参数。
type SearchConfig struct {
	TopK             int     // 截断上限，默认 5
	RecallLimit      int     // 融合重排候选上限，默认 20
	RRFK             int     // 默认 60
	BM25K1           float64 // 默认 1.2
	BM25B            float64 // 默认 0.75
	DefaultThreshold float64 // 默认 0.3
}

func DefaultSearchConfig() SearchConfig {
	return SearchConfig{
		TopK:             5,
		RecallLimit:      20,
		RRFK:             60,
		BM25K1:           1.2,
		BM25B:            0.75,
		DefaultThreshold: 0.3,
	}
}

func (c SearchConfig) withDefaults() SearchConfig {
	d := DefaultSearchConfig()
	if c.TopK <= 0 {
		c.TopK = d.TopK
	}
	if c.RecallLimit <= 0 {
		c.RecallLimit = d.RecallLimit
	}
	if c.RRFK <= 0 {
		c.RRFK = d.RRFK
	}
	if c.BM25K1 <= 0 {
		c.BM25K1 = d.BM25K1
	}
	if c.BM25B <= 0 {
		c.BM25B = d.BM25B
	}
	if c.DefaultThreshold <= 0 {
		c.DefaultThreshold = d.DefaultThreshold
	}
	return c
}

// SearchResult 返回最终截断后的有序切片集合及状态标记。
type SearchResult struct {
	Hits       []Hit
	Reranked   bool
	Generation int64
}

// ModelPort 是检索引擎所需的最小模型端口，由 provider.Gateway 直接满足。
type ModelPort interface {
	ActiveGeneration(ctx context.Context) (int64, error)
	Embed(ctx context.Context, meta provider.CallMeta, inputs []string) ([][]float32, error)
	Rerank(ctx context.Context, meta provider.CallMeta, query string, docs []string) ([]int, string)
}

// Corpus 封装存储层的向量召回、关键词召回及按范围水合子父块文本。
type Corpus interface {
	VectorRecall(ctx context.Context, scope Scope, vec []float32, gen int64, threshold float64, limit int) ([]string, error)
	KeywordRecall(ctx context.Context, scope Scope, query string, k1, b float64, limit int) ([]string, error)
	LoadChunks(ctx context.Context, scope Scope, chunkIDs []string) ([]Hit, error)
}

// QuestionRecaller 为 Slice 2 预生成问法索引保留可选扩展钩子，当前表结构下允许为 nil。
type QuestionRecaller interface {
	QuestionRecall(ctx context.Context, scope Scope, vec []float32, gen int64, query string, limit int) ([]string, error)
}

type EngineOption func(*Engine)

func WithQuestionRecaller(q QuestionRecaller) EngineOption {
	return func(e *Engine) {
		e.questions = q
	}
}

// Engine 统一封装混合检索、RRF 融合、重排降级与父块水合。
type Engine struct {
	corpus    Corpus
	models    ModelPort
	cfg       SearchConfig
	questions QuestionRecaller
}

func NewEngine(corpus Corpus, models ModelPort, cfg SearchConfig, opts ...EngineOption) *Engine {
	e := &Engine{
		corpus: corpus,
		models: models,
		cfg:    cfg.withDefaults(),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(e)
		}
	}
	return e
}

// Search 执行“向量计算 -> 并行多路召回 -> RRF 融合 -> 可选重排 -> TopK 截断 -> 父块装配”。
func (e *Engine) Search(ctx context.Context, reqID string, scope Scope, query string) (SearchResult, error) {
	if scope == nil || len(scope.DocIDs()) == 0 || strings.TrimSpace(query) == "" {
		return SearchResult{}, nil
	}
	gen, _ := e.models.ActiveGeneration(ctx)
	meta := provider.CallMeta{
		RequestID: reqID,
		UserID:    scope.OwnerID(),
	}

	var queryVec []float32
	if vecs, err := e.models.Embed(ctx, meta, []string{query}); err == nil && len(vecs) == 1 && len(vecs[0]) > 0 {
		queryVec = vecs[0]
	}

	threshold := scope.ThresholdOr(e.cfg.DefaultThreshold)
	var (
		vecIDs, kwIDs, qIDs []string
		vecErr, kwErr       error
		wg                  sync.WaitGroup
	)
	if len(queryVec) > 0 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			vecIDs, vecErr = e.corpus.VectorRecall(ctx, scope, queryVec, gen, threshold, e.cfg.RecallLimit)
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		kwIDs, kwErr = e.corpus.KeywordRecall(ctx, scope, query, e.cfg.BM25K1, e.cfg.BM25B, e.cfg.RecallLimit)
	}()
	if e.questions != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			qIDs, _ = e.questions.QuestionRecall(ctx, scope, queryVec, gen, query, e.cfg.RecallLimit)
		}()
	}
	wg.Wait()

	if vecErr != nil && kwErr != nil {
		return SearchResult{Generation: gen}, kwErr
	}

	fused := fuseCandidates(e.cfg.RRFK, e.cfg.RecallLimit, vecIDs, kwIDs, qIDs)
	if len(fused) == 0 {
		return SearchResult{Generation: gen}, nil
	}

	hits, err := e.corpus.LoadChunks(ctx, scope, fused)
	if err != nil {
		return SearchResult{Generation: gen}, err
	}
	if len(hits) == 0 {
		return SearchResult{Generation: gen}, nil
	}

	reranked := false
	docs := make([]string, len(hits))
	for i, h := range hits {
		docs[i] = h.Text
	}
	if order, status := e.models.Rerank(ctx, meta, query, docs); status == "ok" && len(order) > 0 {
		hits = reorderHits(hits, order)
		reranked = true
	}

	if len(hits) > e.cfg.TopK {
		hits = slices.Clone(hits[:e.cfg.TopK])
	}
	return SearchResult{
		Hits:       hits,
		Reranked:   reranked,
		Generation: gen,
	}, nil
}

// Hydrate 复用同一语料库所有权/范围约束，为图谱补充 ID 装配子块与父块文本。
func (e *Engine) Hydrate(ctx context.Context, scope Scope, chunkIDs []string) ([]Hit, error) {
	if scope == nil || len(chunkIDs) == 0 || len(scope.DocIDs()) == 0 {
		return nil, nil
	}
	return e.corpus.LoadChunks(ctx, scope, chunkIDs)
}

func fuseCandidates(rrfK, limit int, lists ...[]string) []string {
	nonEmpty := make([][]string, 0, len(lists))
	for _, list := range lists {
		if len(list) > 0 {
			nonEmpty = append(nonEmpty, list)
		}
	}
	if len(nonEmpty) == 0 {
		return nil
	}
	merged := textutil.RRF(nonEmpty, rrfK)
	if limit > 0 && len(merged) > limit {
		merged = merged[:limit]
	}
	return merged
}

func reorderHits(hits []Hit, order []int) []Hit {
	out := make([]Hit, 0, len(hits))
	used := make([]bool, len(hits))
	for _, idx := range order {
		if idx < 0 || idx >= len(hits) || used[idx] {
			continue
		}
		used[idx] = true
		out = append(out, hits[idx])
	}
	for i, h := range hits {
		if !used[i] {
			out = append(out, h)
		}
	}
	return out
}
