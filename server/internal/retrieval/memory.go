package retrieval

import (
	"context"
	"math"
	"sort"

	"yanzhi/server/internal/textutil"
)

// ChunkSnapshot 表示内存语料库中某个已就绪切块及其父块上下文。
type ChunkSnapshot struct {
	ChunkID             string
	DocumentID          string
	Title               string
	PageStart           int
	ParagraphIndex      int
	Text                string
	ParentText          string
	Embedding           []float32
	EmbeddingGeneration int64
	Terms               map[string]int
	TermCount           int
}

// ChunkSource 提供作用域收紧后的只读切片快照，保持 retrieval 对 knowledge 零反向依赖。
type ChunkSource interface {
	ScopedChunks(owner string, docIDs []string) []ChunkSnapshot
}

type ChunkSourceFunc func(owner string, docIDs []string) []ChunkSnapshot

func (f ChunkSourceFunc) ScopedChunks(owner string, docIDs []string) []ChunkSnapshot {
	return f(owner, docIDs)
}

// MemoryCorpus 在不依赖 PostgreSQL 的环境下实现余弦相似度召回与真实 BM25 关键词召回。
type MemoryCorpus struct {
	source ChunkSource
}

func NewMemoryCorpus(source ChunkSource) *MemoryCorpus {
	return &MemoryCorpus{source: source}
}

func (m *MemoryCorpus) VectorRecall(ctx context.Context, scope Scope, vec []float32, gen int64, threshold float64, limit int) ([]string, error) {
	if len(vec) == 0 || m.source == nil {
		return nil, nil
	}
	snapshots := m.source.ScopedChunks(scope.OwnerID(), scope.DocIDs())
	type scored struct {
		id  string
		sim float64
	}
	var cands []scored
	for _, snap := range snapshots {
		if snap.EmbeddingGeneration != gen || len(snap.Embedding) == 0 || len(snap.Embedding) != len(vec) {
			continue
		}
		sim := cosineSimilarity(vec, snap.Embedding)
		if sim >= threshold {
			cands = append(cands, scored{id: snap.ChunkID, sim: sim})
		}
	}
	sort.Slice(cands, func(i, j int) bool {
		if cands[i].sim == cands[j].sim {
			return cands[i].id < cands[j].id
		}
		return cands[i].sim > cands[j].sim
	})
	if limit > 0 && len(cands) > limit {
		cands = cands[:limit]
	}
	out := make([]string, len(cands))
	for i, c := range cands {
		out[i] = c.id
	}
	return out, nil
}

func (m *MemoryCorpus) KeywordRecall(ctx context.Context, scope Scope, query string, k1, b float64, limit int) ([]string, error) {
	if m.source == nil {
		return nil, nil
	}
	terms := textutil.Terms(query)
	if len(terms) == 0 {
		return nil, nil
	}
	uniqueTerms := make([]string, 0, len(terms))
	seenTerm := make(map[string]struct{}, len(terms))
	for _, t := range terms {
		if _, ok := seenTerm[t]; ok {
			continue
		}
		seenTerm[t] = struct{}{}
		uniqueTerms = append(uniqueTerms, t)
	}

	snapshots := m.source.ScopedChunks(scope.OwnerID(), scope.DocIDs())
	if len(snapshots) == 0 {
		return nil, nil
	}
	nDocs := float64(len(snapshots))
	var totalLen int
	df := make(map[string]int, len(uniqueTerms))
	for _, snap := range snapshots {
		totalLen += snap.TermCount
		for _, t := range uniqueTerms {
			if snap.Terms[t] > 0 {
				df[t]++
			}
		}
	}
	avgLen := float64(totalLen) / nDocs

	type scored struct {
		id    string
		score float64
	}
	var cands []scored
	for _, snap := range snapshots {
		var score float64
		for _, t := range uniqueTerms {
			tf := snap.Terms[t]
			if tf == 0 {
				continue
			}
			score += textutil.BM25(float64(tf), float64(df[t]), float64(snap.TermCount), avgLen, nDocs, k1, b)
		}
		if score > 0 {
			cands = append(cands, scored{id: snap.ChunkID, score: score})
		}
	}
	sort.Slice(cands, func(i, j int) bool {
		if cands[i].score == cands[j].score {
			return cands[i].id < cands[j].id
		}
		return cands[i].score > cands[j].score
	})
	if limit > 0 && len(cands) > limit {
		cands = cands[:limit]
	}
	out := make([]string, len(cands))
	for i, c := range cands {
		out[i] = c.id
	}
	return out, nil
}

func (m *MemoryCorpus) LoadChunks(ctx context.Context, scope Scope, chunkIDs []string) ([]Hit, error) {
	if m.source == nil || len(chunkIDs) == 0 {
		return nil, nil
	}
	snapshots := m.source.ScopedChunks(scope.OwnerID(), scope.DocIDs())
	byID := make(map[string]ChunkSnapshot, len(snapshots))
	for _, snap := range snapshots {
		byID[snap.ChunkID] = snap
	}

	out := make([]Hit, 0, len(chunkIDs))
	seen := make(map[string]struct{}, len(chunkIDs))
	for _, cid := range chunkIDs {
		if _, dup := seen[cid]; dup {
			continue
		}
		seen[cid] = struct{}{}
		if snap, ok := byID[cid]; ok {
			out = append(out, Hit{
				ChunkID:    snap.ChunkID,
				DocumentID: snap.DocumentID,
				Title:      snap.Title,
				PageStart:  snap.PageStart,
				Text:       snap.Text,
				ParentText: snap.ParentText,
			})
		}
	}
	return out, nil
}

func cosineSimilarity(a, b []float32) float64 {
	var dot, normA, normB float64
	for i := range a {
		fa := float64(a[i])
		fb := float64(b[i])
		dot += fa * fb
		normA += fa * fa
		normB += fb * fb
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return dot / (math.Sqrt(normA) * math.Sqrt(normB))
}
