package reading

import (
	"context"
	"sync"

	"yanzhi/server/internal/apperr"
	"yanzhi/server/internal/knowledge"
)

type memoryPageKey struct {
	owner string
	docID string
	page  int
}

type memoryTranslationRecord struct {
	sourceSHA256 string
	translation  string
}

// MemoryStore 基于 knowledge.MemoryStore 与内存 map 实现 Store，供单测和 --in-memory 模式使用。
type MemoryStore struct {
	kb           *knowledge.MemoryStore
	mu           sync.RWMutex
	translations map[memoryPageKey]memoryTranslationRecord
}

// NewMemoryStore 创建内存按页阅读存储。
func NewMemoryStore(kb *knowledge.MemoryStore) *MemoryStore {
	return &MemoryStore{
		kb:           kb,
		translations: make(map[memoryPageKey]memoryTranslationRecord),
	}
}

func (m *MemoryStore) GetDocMeta(_ context.Context, owner, docID string) (DocMeta, error) {
	if m.kb == nil {
		return DocMeta{}, apperr.ErrNotFound
	}
	meta, ok := m.kb.ReadingDocMeta(owner, docID)
	if !ok {
		return DocMeta{}, apperr.ErrNotFound
	}
	return DocMeta{
		ID:               meta.ID,
		OwnerID:          meta.OwnerID,
		Title:            meta.Title,
		SourceType:       meta.SourceType,
		Status:           meta.Status,
		PageCount:        meta.PageCount,
		HasPhysicalPages: meta.HasPhysicalPages,
		HasOriginalFile:  meta.HasOriginalFile,
	}, nil
}

func (m *MemoryStore) ListPageChunks(_ context.Context, owner, docID string, page int) ([]PageChunk, error) {
	if m.kb == nil {
		return nil, apperr.ErrNotFound
	}
	items, err := m.kb.ReadingPageChunks(owner, docID, page)
	if err != nil {
		return nil, err
	}
	out := make([]PageChunk, len(items))
	for i, it := range items {
		out[i] = PageChunk{Text: it.Text}
	}
	return out, nil
}

func (m *MemoryStore) GetTranslation(_ context.Context, owner, docID string, page int, sourceSHA256 string) (string, bool, error) {
	key := memoryPageKey{owner: owner, docID: docID, page: page}
	m.mu.Lock()
	defer m.mu.Unlock()

	rec, ok := m.translations[key]
	if !ok {
		return "", false, nil
	}
	if rec.sourceSHA256 != sourceSHA256 {
		delete(m.translations, key)
		return "", false, nil
	}
	return rec.translation, true, nil
}

func (m *MemoryStore) SaveTranslation(_ context.Context, owner, docID string, page int, sourceSHA256, translation string) error {
	if _, err := m.GetDocMeta(context.Background(), owner, docID); err != nil {
		return err
	}
	key := memoryPageKey{owner: owner, docID: docID, page: page}
	m.mu.Lock()
	m.translations[key] = memoryTranslationRecord{
		sourceSHA256: sourceSHA256,
		translation:  translation,
	}
	m.mu.Unlock()
	return nil
}
