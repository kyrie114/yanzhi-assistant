package suggest

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
)

// SourceDoc 是计算知识库指纹和生成推荐时看到的一份资料。
type SourceDoc struct {
	ID      string
	Version string
	Ready   bool
	Texts   []string
}

// Fingerprint 只哈希已完成资料的 ID 和版本，顺序不影响结果。
func Fingerprint(docs []SourceDoc) string {
	parts := make([]string, 0, len(docs))
	for _, doc := range docs {
		if !doc.Ready {
			continue
		}
		parts = append(parts, doc.ID+"\x00"+doc.Version)
	}
	sort.Strings(parts)
	sum := sha256.Sum256([]byte(strings.Join(parts, "\n")))
	return hex.EncodeToString(sum[:])
}

// UseModel 设置生成推荐问题的模型。没有模型时，过期的知识库只标记为正在更新。
func (m *Memory) UseModel(model QuestionGenerator) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.model = model
}

// SetKBDocuments 记录知识库里的资料。版本没变的已完成资料不会再次生成。
// 未完成的资料不进入指纹，也不会调用模型。
func (m *Memory) SetKBDocuments(kbID, owner string, docs []SourceDoc) {
	copied := cloneDocs(docs)
	m.SetKBSource(kbID, owner, Fingerprint(copied))
	m.mu.Lock()
	row, ok := m.kbs[kbID]
	if !ok || row.OwnerID != owner {
		m.mu.Unlock()
		return
	}
	row.Docs = copied
	m.kbs[kbID] = row
	model := m.model
	m.mu.Unlock()
	if model == nil {
		return
	}
	for _, doc := range copied {
		if m.documentBelongsToOther(doc.ID, owner) {
			continue
		}
		if !doc.Ready {
			m.GenerateDocument(doc.ID, owner, false, nil, nil)
			continue
		}
		if m.documentCurrent(doc.ID, owner, doc.Version) {
			continue
		}
		m.GenerateDocument(doc.ID, owner, true, doc.Texts, model)
		m.markDocumentVersion(doc.ID, owner, doc.Version)
	}
}

func (m *Memory) refreshKB(pending refresh, model QuestionGenerator) {
	texts := make([]string, 0)
	for _, doc := range pending.docs {
		if !doc.Ready {
			continue
		}
		texts = append(texts, doc.Texts...)
	}
	questions, err := model.SuggestQuestions(texts)
	m.SaveKB(pending.id, pending.owner, pending.fingerprint, questions, err != nil)
}

func (m *Memory) documentBelongsToOther(id, owner string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	row, ok := m.docs[id]
	return ok && row.OwnerID != owner
}

func (m *Memory) documentCurrent(id, owner, version string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	row, ok := m.docs[id]
	return ok && row.OwnerID == owner && row.Ready && row.CacheValid && row.CacheFingerprint == version
}

func (m *Memory) markDocumentVersion(id, owner, version string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	row, ok := m.docs[id]
	if !ok || row.OwnerID != owner {
		return
	}
	row.CacheValid = true
	row.CacheFingerprint = version
	m.docs[id] = row
}

func cloneDocs(docs []SourceDoc) []SourceDoc {
	out := make([]SourceDoc, len(docs))
	for i, doc := range docs {
		out[i] = doc
		out[i].Texts = append([]string(nil), doc.Texts...)
	}
	return out
}
