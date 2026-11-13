package knowledge

import (
	"context"
	"encoding/json"
	"maps"
	"slices"

	"github.com/google/uuid"

	"yanzhi/server/internal/apperr"
	"yanzhi/server/internal/textutil"
)

type parentChunkRecord struct {
	ID         string
	DocumentID string
	PageStart  int
	Text       string
}

type chunkRecord struct {
	ID                  string
	DocumentID          string
	ParentID            string
	PageStart           int
	ParagraphIndex      int
	Text                string
	Embedding           []float32
	EmbeddingGeneration int64
	Terms               map[string]int
	TermCount           int
}

// MemoryParentChunk 描述注入内存知识库的父块数据。
type MemoryParentChunk struct {
	ID        string
	PageStart int
	Text      string
}

// MemoryChunkInput 描述注入内存知识库的子块数据。
type MemoryChunkInput struct {
	ID             string
	ParentID       string
	PageStart      int
	ParagraphIndex int
	Text           string
	Embedding      []float32
}

// MemoryChunkSnapshot 是供内存检索引擎只读消费的切块快照，已拼接所属文档当前标题与父块文本。
type MemoryChunkSnapshot struct {
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

func (m *MemoryStore) deleteDocChunksLocked(docID string) {
	for _, cid := range m.docChunks[docID] {
		delete(m.chunks, cid)
	}
	delete(m.docChunks, docID)
	for _, pid := range m.docParents[docID] {
		delete(m.parents, pid)
	}
	delete(m.docParents, docID)
}

// SeedChunks 为测试或内存模式向指定文档写入父子切块与向量并自动统计关键词词频。
func (m *MemoryStore) SeedChunks(owner, docID string, generation int64, parents []MemoryParentChunk, chunks []MemoryChunkInput) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	doc, ok := m.docs[docID]
	if !ok || doc.OwnerID != owner {
		return apperr.ErrNotFound
	}
	m.deleteDocChunksLocked(docID)

	parentIDs := make([]string, 0, len(parents))
	for _, p := range parents {
		pid := p.ID
		if pid == "" {
			pid = uuid.NewString()
		}
		m.parents[pid] = parentChunkRecord{
			ID:         pid,
			DocumentID: docID,
			PageStart:  p.PageStart,
			Text:       p.Text,
		}
		parentIDs = append(parentIDs, pid)
	}
	m.docParents[docID] = parentIDs

	chunkIDs := make([]string, 0, len(chunks))
	for i, c := range chunks {
		cid := c.ID
		if cid == "" {
			cid = uuid.NewString()
		}
		termsList := textutil.Terms(c.Text)
		tf := make(map[string]int, len(termsList))
		for _, term := range termsList {
			tf[term]++
		}
		para := c.ParagraphIndex
		if para == 0 {
			para = i + 1
		}
		vec := slices.Clone(c.Embedding)
		m.chunks[cid] = chunkRecord{
			ID:                  cid,
			DocumentID:          docID,
			ParentID:            c.ParentID,
			PageStart:           c.PageStart,
			ParagraphIndex:      para,
			Text:                c.Text,
			Embedding:           vec,
			EmbeddingGeneration: generation,
			Terms:               tf,
			TermCount:           len(termsList),
		}
		chunkIDs = append(chunkIDs, cid)
	}
	m.docChunks[docID] = chunkIDs
	return nil
}

// SetDocStatus 更新内存文档状态（供单测与 --in-memory 流水线推进 ready/pending 状态）。
func (m *MemoryStore) SetDocStatus(owner, docID, status string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	doc, ok := m.docs[docID]
	if !ok || doc.OwnerID != owner {
		return apperr.ErrNotFound
	}
	doc.Status = status
	doc.UpdatedAt = m.clock.Now().UTC()
	m.docs[docID] = doc
	return nil
}

// PatchKB 更新内存知识库的回答说明、检索阈值或图谱开关，校验规则与 PG Service.PatchKB 保持一致。
func (m *MemoryStore) PatchKB(ctx context.Context, owner, id string, raw map[string]json.RawMessage) (KB, error) {
	m.mu.Lock()
	kb, ok := m.kbs[id]
	if !ok || kb.OwnerID != owner {
		m.mu.Unlock()
		return KB{}, apperr.ErrNotFound
	}

	if v, exists := raw["answer_instruction"]; exists {
		if string(v) == "null" {
			kb.AnswerInstruction = nil
		} else {
			var text string
			if err := json.Unmarshal(v, &text); err != nil {
				m.mu.Unlock()
				return KB{}, apperr.WithDetails("VALIDATION_ERROR", "回答说明格式不正确", map[string]any{"field": "answer_instruction"})
			}
			if len([]rune(text)) > 2000 {
				m.mu.Unlock()
				return KB{}, apperr.WithDetails("VALIDATION_ERROR", "回答说明不能超过 2000 个字符", map[string]any{"field": "answer_instruction"})
			}
			kb.AnswerInstruction = &text
		}
	}
	if v, exists := raw["retrieval_threshold"]; exists {
		if string(v) == "null" {
			kb.RetrievalThreshold = nil
		} else {
			var n float64
			if err := json.Unmarshal(v, &n); err != nil || n < 0 || n > 1 {
				m.mu.Unlock()
				return KB{}, apperr.WithDetails("VALIDATION_ERROR", "检索阈值需要在 0 到 1 之间", map[string]any{"field": "retrieval_threshold"})
			}
			kb.RetrievalThreshold = &n
		}
	}
	if v, exists := raw["graph_enabled"]; exists {
		enabled := true
		if string(v) != "null" {
			if err := json.Unmarshal(v, &enabled); err != nil {
				m.mu.Unlock()
				return KB{}, apperr.WithDetails("VALIDATION_ERROR", "图谱开关格式不正确", map[string]any{"field": "graph_enabled"})
			}
		}
		kb.GraphEnabled = enabled
	}
	kb.UpdatedAt = m.clock.Now().UTC()
	m.kbs[id] = kb
	m.mu.Unlock()

	return m.GetKB(ctx, owner, id)
}

// ResolveScope 按参数(422) -> 目标归属(404) -> 就绪文档筛选(409) 的顺序解析内存范围。
func (m *MemoryStore) ResolveScope(ctx context.Context, owner string, rawIn ScopeInput) (ResolvedScope, error) {
	in, err := validateScopeInput(rawIn)
	if err != nil {
		return ResolvedScope{}, err
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	resolved := ResolvedScope{
		ownerID: owner,
		input:   in,
	}

	var matchedDocs []docRecord
	switch in.Type {
	case "kb":
		kb, ok := m.kbs[in.KBID]
		if !ok || kb.OwnerID != owner {
			return ResolvedScope{}, apperr.ErrNotFound
		}
		resolved.singleKB = true
		if kb.AnswerInstruction != nil {
			resolved.answerInstruction = *kb.AnswerInstruction
		}
		resolved.threshold = kb.RetrievalThreshold
		for _, doc := range m.docs {
			if doc.OwnerID == owner && doc.KBID == kb.ID && doc.Status == "ready" {
				matchedDocs = append(matchedDocs, doc)
			}
		}
	case "tags":
		tagSet := make(map[string]struct{}, len(in.TagIDs))
		for _, tid := range in.TagIDs {
			tag, ok := m.tags[tid]
			if !ok || tag.OwnerID != owner {
				return ResolvedScope{}, apperr.ErrNotFound
			}
			tagSet[tid] = struct{}{}
		}
		seen := make(map[string]struct{})
		for tid := range tagSet {
			for docID := range m.tagDocs[tid] {
				if _, already := seen[docID]; already {
					continue
				}
				seen[docID] = struct{}{}
				doc, ok := m.docs[docID]
				if ok && doc.OwnerID == owner && doc.Status == "ready" {
					matchedDocs = append(matchedDocs, doc)
				}
			}
		}
	case "documents":
		docSet := make(map[string]struct{}, len(in.DocumentIDs))
		for _, did := range in.DocumentIDs {
			doc, ok := m.docs[did]
			if !ok || doc.OwnerID != owner {
				return ResolvedScope{}, apperr.ErrNotFound
			}
			docSet[did] = struct{}{}
		}
		for did := range docSet {
			doc := m.docs[did]
			if doc.Status == "ready" {
				matchedDocs = append(matchedDocs, doc)
			}
		}
	default: // "all"
		for _, doc := range m.docs {
			if doc.OwnerID == owner && doc.Status == "ready" {
				matchedDocs = append(matchedDocs, doc)
			}
		}
	}

	if len(matchedDocs) == 0 {
		return ResolvedScope{}, apperr.ErrNoReadyDocs
	}
	slices.SortFunc(matchedDocs, func(a, b docRecord) int {
		if a.ID < b.ID {
			return -1
		}
		if a.ID > b.ID {
			return 1
		}
		return 0
	})
	for _, doc := range matchedDocs {
		resolved.docIDs = append(resolved.docIDs, doc.ID)
		if kb, ok := m.kbs[doc.KBID]; ok && kb.GraphEnabled {
			resolved.graphDocIDs = append(resolved.graphDocIDs, doc.ID)
		}
	}
	return resolved, nil
}

// ChunkDetail 返回指定内存文档下某个切片及其所属父块文本。
func (m *MemoryStore) ChunkDetail(ctx context.Context, owner, docID, chunkID string) (ChunkDetail, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	doc, ok := m.docs[docID]
	if !ok || doc.OwnerID != owner {
		return ChunkDetail{}, apperr.ErrNotFound
	}
	ch, ok := m.chunks[chunkID]
	if !ok || ch.DocumentID != docID {
		return ChunkDetail{}, apperr.ErrNotFound
	}
	var parentText string
	if ch.ParentID != "" {
		if p, exists := m.parents[ch.ParentID]; exists && p.DocumentID == docID {
			parentText = p.Text
		}
	}
	return ChunkDetail{
		Text:       ch.Text,
		PageStart:  ch.PageStart,
		ParentText: parentText,
	}, nil
}

// ScopedChunkSnapshots 返回允许范围且状态仍为 ready 的全部子块快照（每次读取动态取最新文档标题）。
func (m *MemoryStore) ScopedChunkSnapshots(owner string, docIDs []string) []MemoryChunkSnapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()

	out := make([]MemoryChunkSnapshot, 0)
	for _, docID := range docIDs {
		doc, ok := m.docs[docID]
		if !ok || doc.OwnerID != owner || doc.Status != "ready" {
			continue
		}
		for _, cid := range m.docChunks[docID] {
			ch, ok := m.chunks[cid]
			if !ok {
				continue
			}
			var parentText string
			if ch.ParentID != "" {
				if p, exists := m.parents[ch.ParentID]; exists && p.DocumentID == docID {
					parentText = p.Text
				}
			}
			out = append(out, MemoryChunkSnapshot{
				ChunkID:             ch.ID,
				DocumentID:          doc.ID,
				Title:               doc.Title,
				PageStart:           ch.PageStart,
				ParagraphIndex:      ch.ParagraphIndex,
				Text:                ch.Text,
				ParentText:          parentText,
				Embedding:           slices.Clone(ch.Embedding),
				EmbeddingGeneration: ch.EmbeddingGeneration,
				Terms:               maps.Clone(ch.Terms),
				TermCount:           ch.TermCount,
			})
		}
	}
	return out
}
