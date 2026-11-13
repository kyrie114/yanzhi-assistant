package qa

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"yanzhi/server/internal/apperr"
	"yanzhi/server/internal/knowledge"
	"yanzhi/server/internal/platform/clock"
	"yanzhi/server/internal/platform/db"
	"yanzhi/server/internal/retrieval"
)

type memSession struct {
	ID        string
	OwnerID   string
	Title     string
	CreatedAt time.Time
	UpdatedAt time.Time
	Turns     []memTurn
}

type memTurn struct {
	ID         string
	Question   string
	Answer     string
	AnswerKind string
	Rewritten  bool
	Reranked   bool
	CreatedAt  time.Time
	Citations  []PersistCitation
}

// MemoryStoreOption 配置内存 QA 存储的可选钩子。
type MemoryStoreOption func(*MemoryStore)

// WithAliveCheck 注入文档/切片存活检查函数，以便在引用原文档或切片被删除后规范化历史引用标签。
func WithAliveCheck(fn func(owner, docID, chunkID string) bool) MemoryStoreOption {
	return func(s *MemoryStore) {
		s.aliveCheck = fn
	}
}

// MemoryStore 为单测与 `--in-memory` 模式提供与 PGStore 语义一致的并发安全内存存储。
type MemoryStore struct {
	mu         sync.RWMutex
	sessions   map[string]*memSession
	turnIndex  map[string]memTurnRef
	aliveCheck func(owner, docID, chunkID string) bool
}

type memTurnRef struct {
	ownerID   string
	sessionID string
}

func NewMemoryStore(opts ...MemoryStoreOption) *MemoryStore {
	s := &MemoryStore{
		sessions:  make(map[string]*memSession),
		turnIndex: make(map[string]memTurnRef),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(s)
		}
	}
	return s
}

func (s *MemoryStore) CreateSession(_ context.Context, owner, title string) (Session, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		title = "新会话"
	}
	if len([]rune(title)) > 80 {
		return Session{}, apperr.WithDetails("VALIDATION_ERROR", "标题不能超过 80 个字符", map[string]any{"field": "title"})
	}
	now := time.Now().UTC()
	id := db.NewID()

	s.mu.Lock()
	s.sessions[id] = &memSession{
		ID:        id,
		OwnerID:   owner,
		Title:     title,
		CreatedAt: now,
		UpdatedAt: now,
	}
	s.mu.Unlock()

	ts := clock.Format(now)
	return Session{
		ID:        id,
		Title:     title,
		CreatedAt: ts,
		UpdatedAt: ts,
	}, nil
}

func (s *MemoryStore) ListSessions(_ context.Context, owner string, page, size int) ([]Session, int, error) {
	if page < 1 {
		page = 1
	}
	if size < 1 || size > 100 {
		size = 20
	}

	s.mu.RLock()
	owned := make([]*memSession, 0)
	for _, sess := range s.sessions {
		if sess.OwnerID == owner {
			owned = append(owned, sess)
		}
	}
	sort.Slice(owned, func(i, j int) bool {
		if !owned[i].UpdatedAt.Equal(owned[j].UpdatedAt) {
			return owned[i].UpdatedAt.After(owned[j].UpdatedAt)
		}
		return owned[i].ID > owned[j].ID
	})
	total := len(owned)
	start := (page - 1) * size
	if start >= total {
		s.mu.RUnlock()
		return []Session{}, total, nil
	}
	end := min(start+size, total)
	out := make([]Session, 0, end-start)
	for _, sess := range owned[start:end] {
		out = append(out, Session{
			ID:        sess.ID,
			Title:     sess.Title,
			CreatedAt: clock.Format(sess.CreatedAt),
			UpdatedAt: clock.Format(sess.UpdatedAt),
		})
	}
	s.mu.RUnlock()
	return out, total, nil
}

func (s *MemoryStore) GetSession(_ context.Context, owner, id string) (Session, error) {
	s.mu.RLock()
	sess, ok := s.sessions[id]
	if !ok || sess.OwnerID != owner {
		s.mu.RUnlock()
		return Session{}, apperr.ErrNotFound
	}
	out := Session{
		ID:        sess.ID,
		Title:     sess.Title,
		CreatedAt: clock.Format(sess.CreatedAt),
		UpdatedAt: clock.Format(sess.UpdatedAt),
		Turns:     make([]Turn, 0, len(sess.Turns)),
	}
	alive := s.aliveCheck
	// 先在读锁内拷贝全部值快照，防止在锁外遍历时发生数据竞争，也避免在读锁内回调外部 store。
	turnsSnap := make([]memTurn, len(sess.Turns))
	for i, t := range sess.Turns {
		tCopy := t
		tCopy.Citations = append([]PersistCitation(nil), t.Citations...)
		turnsSnap[i] = tCopy
	}
	s.mu.RUnlock()

	for _, t := range turnsSnap {
		cites := make([]Citation, 0, len(t.Citations))
		for _, c := range t.Citations {
			var wire Citation
			wire.Origin = c.Origin
			wire.GraphSupplement = c.GraphSupplement
			wire.Label = c.Label
			if c.Origin == "kb" && (c.ChunkID == "" || c.DocumentID == "" || (alive != nil && !alive(owner, c.DocumentID, c.ChunkID))) {
				wire.ChunkID = nil
				wire.DocumentID = nil
				wire.Label = "来源已删除"
			} else {
				if c.ChunkID != "" {
					cid := c.ChunkID
					wire.ChunkID = &cid
				}
				if c.DocumentID != "" {
					did := c.DocumentID
					wire.DocumentID = &did
				}
			}
			cites = append(cites, wire)
		}
		out.Turns = append(out.Turns, Turn{
			ID:         t.ID,
			Question:   t.Question,
			Answer:     t.Answer,
			AnswerKind: t.AnswerKind,
			Rewritten:  t.Rewritten,
			Reranked:   t.Reranked,
			CreatedAt:  clock.Format(t.CreatedAt),
			Citations:  cites,
		})
	}
	return out, nil
}

func (s *MemoryStore) EnsureSessionOwner(_ context.Context, owner, id string) (string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sess, ok := s.sessions[id]
	if !ok || sess.OwnerID != owner {
		return "", apperr.ErrNotFound
	}
	return sess.Title, nil
}

func (s *MemoryStore) PatchSession(ctx context.Context, owner, id, title string) (Session, error) {
	title = strings.TrimSpace(title)
	if title == "" || len([]rune(title)) > 80 {
		return Session{}, apperr.WithDetails("VALIDATION_ERROR", "标题需要 1 到 80 个字符", map[string]any{"field": "title"})
	}
	s.mu.Lock()
	sess, ok := s.sessions[id]
	if !ok || sess.OwnerID != owner {
		s.mu.Unlock()
		return Session{}, apperr.ErrNotFound
	}
	sess.Title = title
	sess.UpdatedAt = time.Now().UTC()
	s.mu.Unlock()
	return s.GetSession(ctx, owner, id)
}

func (s *MemoryStore) DeleteSession(_ context.Context, owner, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[id]
	if !ok || sess.OwnerID != owner {
		return apperr.ErrNotFound
	}
	for _, t := range sess.Turns {
		delete(s.turnIndex, t.ID)
	}
	delete(s.sessions, id)
	return nil
}

func (s *MemoryStore) RecentHistory(_ context.Context, owner, sessionID string, limit int) ([]HistoryTurn, error) {
	if limit <= 0 {
		limit = 5
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	sess, ok := s.sessions[sessionID]
	if !ok || sess.OwnerID != owner {
		return nil, apperr.ErrNotFound
	}
	n := len(sess.Turns)
	if n == 0 {
		return []HistoryTurn{}, nil
	}
	start := 0
	if n > limit {
		start = n - limit
	}
	out := make([]HistoryTurn, 0, n-start)
	for _, t := range sess.Turns[start:] {
		out = append(out, HistoryTurn{
			Question: t.Question,
			Answer:   t.Answer,
		})
	}
	return out, nil
}

func (s *MemoryStore) SaveTurn(_ context.Context, currentTitle string, turn PersistTurn) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[turn.SessionID]
	if !ok {
		return apperr.ErrNotFound
	}
	cites := make([]PersistCitation, len(turn.Citations))
	copy(cites, turn.Citations)

	ts := turn.CreatedAt.UTC()
	if ts.IsZero() {
		ts = time.Now().UTC()
	}
	sess.Turns = append(sess.Turns, memTurn{
		ID:         turn.ID,
		Question:   turn.Question,
		Answer:     turn.Answer,
		AnswerKind: turn.AnswerKind,
		Rewritten:  turn.Rewritten,
		Reranked:   turn.Reranked,
		CreatedAt:  ts,
		Citations:  cites,
	})
	s.turnIndex[turn.ID] = memTurnRef{
		ownerID:   sess.OwnerID,
		sessionID: sess.ID,
	}
	if sess.Title == "新会话" {
		sess.Title = autoSessionTitle(currentTitle, turn.Question)
	}
	sess.UpdatedAt = ts
	return nil
}

func (s *MemoryStore) TurnExists(_ context.Context, owner, sessionID, turnID string) (bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ref, ok := s.turnIndex[turnID]
	return ok && ref.ownerID == owner && ref.sessionID == sessionID, nil
}

// MemoryAliveChecker 返回关联到 knowledge.MemoryStore 的引用存活判断函数。
func MemoryAliveChecker(kb *knowledge.MemoryStore) func(owner, docID, chunkID string) bool {
	if kb == nil {
		return nil
	}
	return func(owner, docID, chunkID string) bool {
		_, err := kb.ChunkDetail(context.Background(), owner, docID, chunkID)
		return err == nil
	}
}

// MemoryChunkSource 将 knowledge.MemoryStore 的作用域切片快照适配为 retrieval.ChunkSource。
func MemoryChunkSource(kb *knowledge.MemoryStore) retrieval.ChunkSource {
	return retrieval.ChunkSourceFunc(func(owner string, docIDs []string) []retrieval.ChunkSnapshot {
		if kb == nil {
			return nil
		}
		snaps := kb.ScopedChunkSnapshots(owner, docIDs)
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
