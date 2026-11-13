package retrieval_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"yanzhi/server/internal/knowledge"
	"yanzhi/server/internal/provider"
	"yanzhi/server/internal/retrieval"
)

func knowledgeSource(store *knowledge.MemoryStore) retrieval.ChunkSource {
	return retrieval.ChunkSourceFunc(func(owner string, docIDs []string) []retrieval.ChunkSnapshot {
		snaps := store.ScopedChunkSnapshots(owner, docIDs)
		out := make([]retrieval.ChunkSnapshot, len(snaps))
		for i, s := range snaps {
			out[i] = retrieval.ChunkSnapshot{
				ChunkID:             s.ChunkID,
				DocumentID:          s.DocumentID,
				Title:               s.Title,
				PageStart:           s.PageStart,
				ParagraphIndex:      s.ParagraphIndex,
				Text:                s.Text,
				ParentText:          s.ParentText,
				Embedding:           s.Embedding,
				EmbeddingGeneration: s.EmbeddingGeneration,
				Terms:               s.Terms,
				TermCount:           s.TermCount,
			}
		}
		return out
	})
}

func seedTestLibrary(t *testing.T) (*knowledge.MemoryStore, string, knowledge.KB, string) {
	t.Helper()
	store := knowledge.NewMemoryStore(nil, nil, nil)
	ctx := context.Background()
	owner := "user-1"
	kb, err := store.CreateKB(ctx, owner, "测试库")
	if err != nil {
		t.Fatal(err)
	}
	pdf := []byte("%PDF-1.4\n1 0 obj\n<< /Type /Page >>\nendobj\n")
	up, err := store.Upload(ctx, owner, kb.ID, knowledge.UploadMeta{
		FileName: "paper.pdf", Title: "Attention Paper",
	}, pdf)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetDocStatusForTest(owner, up.Doc.ID, "ready", nil); err != nil {
		t.Fatal(err)
	}
	return store, owner, kb, up.Doc.ID
}

func TestEngineSearchRerankFallbackAndTopK(t *testing.T) {
	store, owner, kb, docID := seedTestLibrary(t)
	ctx := context.Background()

	var inputs []knowledge.MemoryChunkInput
	for i := 1; i <= 8; i++ {
		simVec := []float32{float32(10 - i), float32(i)}
		txt := fmt.Sprintf("第 %d 段通用语义描述", i)
		if i == 7 {
			txt = "包含专名 AUDIO-LINE-3 的核心段落"
		}
		inputs = append(inputs, knowledge.MemoryChunkInput{
			ID:             fmt.Sprintf("chunk-%d", i),
			ParentID:       "parent-1",
			PageStart:      i + 1,
			ParagraphIndex: i,
			Text:           txt,
			Embedding:      simVec,
		})
	}
	err := store.SeedChunks(owner, docID, 1, []knowledge.MemoryParentChunk{
		{ID: "parent-1", PageStart: 2, Text: "完整父块上下文文本"},
	}, inputs)
	if err != nil {
		t.Fatal(err)
	}

	rawThresh, _ := json.Marshal(0.1)
	if _, err := store.PatchKB(ctx, owner, kb.ID, map[string]json.RawMessage{
		"retrieval_threshold": rawThresh,
	}); err != nil {
		t.Fatal(err)
	}

	resolved, err := store.ResolveScope(ctx, owner, knowledge.ScopeInput{Type: "kb", KBID: kb.ID})
	if err != nil {
		t.Fatal(err)
	}

	gw := provider.NewMemoryGateway()
	gw.EmbedFn = func(ctx context.Context, inputs []string) ([][]float32, error) {
		return [][]float32{{1, 0}}, nil
	}

	engine := retrieval.NewEngine(retrieval.NewMemoryCorpus(knowledgeSource(store)), gw, retrieval.DefaultSearchConfig())

	// 1. Rerank 未配置 -> 回退 RRF 顺序，截断 Top-5，携带父块与专名关键词命中
	res, err := engine.Search(ctx, "req-1", resolved, "AUDIO-LINE-3")
	if err != nil {
		t.Fatal(err)
	}
	if res.Reranked || len(res.Hits) != 5 {
		t.Fatalf("expected reranked=false and 5 hits, got reranked=%v len=%d", res.Reranked, len(res.Hits))
	}
	foundProper := false
	for _, h := range res.Hits {
		if h.ParentText != "完整父块上下文文本" {
			t.Fatalf("expected parent text hydrated, got %q", h.ParentText)
		}
		if h.ChunkID == "chunk-7" {
			foundProper = true
		}
	}
	if !foundProper {
		t.Fatalf("expected proper noun hit chunk-7 to survive via BM25+RRF: %+v", res.Hits)
	}

	// 2. 配置 Rerank 后 -> Reranked=true 且按重排顺序前置尾部候选
	gw.SetModel("rerank", provider.Remote{Model: "bge-rerank"})
	gw.RerankFn = func(ctx context.Context, query string, docs []string) ([]int, error) {
		idx := len(docs) - 1
		return []int{idx, 0, 1}, nil
	}
	res2, err := engine.Search(ctx, "req-2", resolved, "通用语义描述")
	if err != nil {
		t.Fatal(err)
	}
	if !res2.Reranked || len(res2.Hits) != 5 || res2.Hits[0].ChunkID == "chunk-1" {
		t.Fatalf("expected reranked reorder, got reranked=%v hits[0]=%+v", res2.Reranked, res2.Hits[0])
	}

	// 3. Rerank 故障 -> 不报错，自动回退 RRF 并设置 Reranked=false
	gw.RerankFn = func(ctx context.Context, query string, docs []string) ([]int, error) {
		return nil, errors.New("rerank down")
	}
	res3, err := engine.Search(ctx, "req-3", resolved, "通用语义描述")
	if err != nil {
		t.Fatal(err)
	}
	if res3.Reranked || len(res3.Hits) != 5 {
		t.Fatalf("expected graceful fallback to RRF, got reranked=%v len=%d", res3.Reranked, len(res3.Hits))
	}
}

type emptyScope struct{}

func (emptyScope) OwnerID() string                      { return "user-1" }
func (emptyScope) DocIDs() []string                     { return nil }
func (emptyScope) ThresholdOr(fallback float64) float64 { return fallback }

func TestEngineSkipsEmbedWhenScopeEmpty(t *testing.T) {
	gw := provider.NewMemoryGateway()
	embedCalled := false
	gw.EmbedFn = func(ctx context.Context, inputs []string) ([][]float32, error) {
		embedCalled = true
		return [][]float32{{1, 0}}, nil
	}
	engine := retrieval.NewEngine(retrieval.NewMemoryCorpus(nil), gw, retrieval.DefaultSearchConfig())
	res, err := engine.Search(context.Background(), "req-empty", emptyScope{}, "test")
	if err != nil || len(res.Hits) != 0 || embedCalled {
		t.Fatalf("expected empty search without embed call, got err=%v len=%d embedCalled=%v", err, len(res.Hits), embedCalled)
	}
}
