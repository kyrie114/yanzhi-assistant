package qa

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"yanzhi/server/internal/apperr"
	"yanzhi/server/internal/platform/clock"
	"yanzhi/server/internal/platform/db"
)

// PGStore 基于 PostgreSQL 实现 qa.Store。
type PGStore struct {
	Pool *pgxpool.Pool
}

func NewPGStore(pool *pgxpool.Pool) *PGStore {
	return &PGStore{Pool: pool}
}

func (s *PGStore) CreateSession(ctx context.Context, owner, title string) (Session, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		title = "新会话"
	}
	if len([]rune(title)) > 80 {
		return Session{}, apperr.WithDetails("VALIDATION_ERROR", "标题不能超过 80 个字符", map[string]any{"field": "title"})
	}
	id := db.NewID()
	now := time.Now().UTC()
	_, err := s.Pool.Exec(ctx, `
		INSERT INTO qa_sessions (id, owner_id, title, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $4)`,
		db.MustUUID(id), db.MustUUID(owner), title, now)
	if err != nil {
		return Session{}, err
	}
	ts := clock.Format(now)
	return Session{ID: id, Title: title, CreatedAt: ts, UpdatedAt: ts}, nil
}

func (s *PGStore) ListSessions(ctx context.Context, owner string, page, size int) ([]Session, int, error) {
	if page < 1 {
		page = 1
	}
	if size < 1 || size > 100 {
		size = 20
	}
	ownerUUID, ok := db.UUID(owner)
	if !ok {
		return []Session{}, 0, nil
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT id::text, title, created_at, updated_at
		FROM qa_sessions WHERE owner_id = $1
		ORDER BY updated_at DESC, id DESC LIMIT $2 OFFSET $3`, ownerUUID, size, (page-1)*size)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []Session{}
	for rows.Next() {
		var item Session
		var created, updated time.Time
		if err := rows.Scan(&item.ID, &item.Title, &created, &updated); err != nil {
			return nil, 0, err
		}
		item.CreatedAt = clock.Format(created)
		item.UpdatedAt = clock.Format(updated)
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	var total int
	if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM qa_sessions WHERE owner_id = $1`, ownerUUID).Scan(&total); err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

func (s *PGStore) GetSession(ctx context.Context, owner, id string) (Session, error) {
	ownerUUID, okOwner := db.UUID(owner)
	sessUUID, okSess := db.UUID(id)
	if !okOwner || !okSess {
		return Session{}, apperr.ErrNotFound
	}
	var item Session
	var created, updated time.Time
	err := s.Pool.QueryRow(ctx, `
		SELECT id::text, title, created_at, updated_at
		FROM qa_sessions WHERE id = $1 AND owner_id = $2`, sessUUID, ownerUUID).
		Scan(&item.ID, &item.Title, &created, &updated)
	if errors.Is(err, pgx.ErrNoRows) {
		return Session{}, apperr.ErrNotFound
	}
	if err != nil {
		return Session{}, err
	}
	item.CreatedAt = clock.Format(created)
	item.UpdatedAt = clock.Format(updated)

	rows, err := s.Pool.Query(ctx, `
		SELECT id::text, question, answer, answer_kind, rewritten, reranked, created_at
		FROM qa_turns WHERE session_id = $1 ORDER BY created_at, id`, sessUUID)
	if err != nil {
		return Session{}, err
	}
	defer rows.Close()
	item.Turns = []Turn{}
	for rows.Next() {
		var t Turn
		var tCreated time.Time
		if err := rows.Scan(&t.ID, &t.Question, &t.Answer, &t.AnswerKind, &t.Rewritten, &t.Reranked, &tCreated); err != nil {
			return Session{}, err
		}
		t.CreatedAt = clock.Format(tCreated)
		item.Turns = append(item.Turns, t)
	}
	if err := rows.Err(); err != nil {
		return Session{}, err
	}
	for i := range item.Turns {
		cites, err := s.turnCitations(ctx, item.Turns[i].ID)
		if err != nil {
			return Session{}, err
		}
		item.Turns[i].Citations = cites
	}
	return item, nil
}

func (s *PGStore) turnCitations(ctx context.Context, turnID string) ([]Citation, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT qc.chunk_id::text, c.id::text, d.id::text, qc.label, qc.origin, qc.graph_supplement
		FROM qa_citations qc
		LEFT JOIN chunks c ON c.id = qc.chunk_id
		LEFT JOIN documents d ON d.id = qc.document_id
		WHERE qc.turn_id = $1
		ORDER BY qc.id`, db.MustUUID(turnID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Citation{}
	for rows.Next() {
		var c Citation
		var storedChunkID, liveChunkID, liveDocID pgtype.Text
		if err := rows.Scan(&storedChunkID, &liveChunkID, &liveDocID, &c.Label, &c.Origin, &c.GraphSupplement); err != nil {
			return nil, err
		}
		if c.Origin == "kb" && (!storedChunkID.Valid || !liveChunkID.Valid || !liveDocID.Valid) {
			c.ChunkID = nil
			c.DocumentID = nil
			c.Label = "来源已删除"
		} else {
			if liveChunkID.Valid {
				val := liveChunkID.String
				c.ChunkID = &val
			}
			if liveDocID.Valid {
				val := liveDocID.String
				c.DocumentID = &val
			}
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *PGStore) EnsureSessionOwner(ctx context.Context, owner, id string) (string, error) {
	ownerUUID, okOwner := db.UUID(owner)
	sessUUID, okSess := db.UUID(id)
	if !okOwner || !okSess {
		return "", apperr.ErrNotFound
	}
	var title string
	err := s.Pool.QueryRow(ctx, `SELECT title FROM qa_sessions WHERE id = $1 AND owner_id = $2`, sessUUID, ownerUUID).Scan(&title)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", apperr.ErrNotFound
	}
	return title, err
}

func (s *PGStore) PatchSession(ctx context.Context, owner, id, title string) (Session, error) {
	title = strings.TrimSpace(title)
	if title == "" || len([]rune(title)) > 80 {
		return Session{}, apperr.WithDetails("VALIDATION_ERROR", "标题需要 1 到 80 个字符", map[string]any{"field": "title"})
	}
	ownerUUID, okOwner := db.UUID(owner)
	sessUUID, okSess := db.UUID(id)
	if !okOwner || !okSess {
		return Session{}, apperr.ErrNotFound
	}
	tag, err := s.Pool.Exec(ctx, `UPDATE qa_sessions SET title = $3, updated_at = now() WHERE id = $1 AND owner_id = $2`, sessUUID, ownerUUID, title)
	if err != nil {
		return Session{}, err
	}
	if tag.RowsAffected() == 0 {
		return Session{}, apperr.ErrNotFound
	}
	return s.GetSession(ctx, owner, id)
}

func (s *PGStore) DeleteSession(ctx context.Context, owner, id string) error {
	ownerUUID, okOwner := db.UUID(owner)
	sessUUID, okSess := db.UUID(id)
	if !okOwner || !okSess {
		return apperr.ErrNotFound
	}
	tag, err := s.Pool.Exec(ctx, `DELETE FROM qa_sessions WHERE id = $1 AND owner_id = $2`, sessUUID, ownerUUID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return apperr.ErrNotFound
	}
	return nil
}

func (s *PGStore) RecentHistory(ctx context.Context, owner, sessionID string, limit int) ([]HistoryTurn, error) {
	if limit <= 0 {
		limit = 5
	}
	ownerUUID, okOwner := db.UUID(owner)
	sessUUID, okSess := db.UUID(sessionID)
	if !okOwner || !okSess {
		return nil, apperr.ErrNotFound
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT t.question, t.answer
		FROM qa_turns t
		JOIN qa_sessions s ON s.id = t.session_id
		WHERE t.session_id = $1 AND s.owner_id = $2
		ORDER BY t.created_at DESC, t.id DESC
		LIMIT $3`, sessUUID, ownerUUID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var desc []HistoryTurn
	for rows.Next() {
		var h HistoryTurn
		if err := rows.Scan(&h.Question, &h.Answer); err != nil {
			return nil, err
		}
		desc = append(desc, h)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i, j := 0, len(desc)-1; i < j; i, j = i+1, j-1 {
		desc[i], desc[j] = desc[j], desc[i]
	}
	return desc, nil
}

func (s *PGStore) SaveTurn(ctx context.Context, currentTitle string, turn PersistTurn) error {
	scopeBytes, _ := json.Marshal(turn.Scope)
	candUUIDs := uuidSliceOrEmpty(turn.CandidateIDs)
	extraUUIDs := uuidSliceOrEmpty(turn.GraphExtraIDs)

	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	_, err = tx.Exec(ctx, `
		INSERT INTO qa_turns (
			id, session_id, question, answer, answer_kind, rewritten, reranked, web_search,
			scope, candidate_ids, graph_extra_ids, created_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
		db.MustUUID(turn.ID), db.MustUUID(turn.SessionID), turn.Question, turn.Answer,
		turn.AnswerKind, turn.Rewritten, turn.Reranked, turn.WebSearch,
		scopeBytes, candUUIDs, extraUUIDs, turn.CreatedAt.UTC())
	if err != nil {
		return err
	}
	for _, c := range turn.Citations {
		_, err = tx.Exec(ctx, `
			INSERT INTO qa_citations (
				id, turn_id, chunk_id, document_id, title_snapshot, page_start, origin, graph_supplement, label
			) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
			db.MustUUID(db.NewID()), db.MustUUID(turn.ID),
			nullableUUID(c.ChunkID), nullableUUID(c.DocumentID),
			c.TitleSnapshot, c.PageStart, c.Origin, c.GraphSupplement, c.Label)
		if err != nil {
			return err
		}
	}
	nextTitle := autoSessionTitle(currentTitle, turn.Question)
	if _, err := tx.Exec(ctx, `UPDATE qa_sessions SET title = $2, updated_at = $3 WHERE id = $1`,
		db.MustUUID(turn.SessionID), nextTitle, turn.CreatedAt.UTC()); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *PGStore) TurnExists(ctx context.Context, owner, sessionID, turnID string) (bool, error) {
	ownerUUID, okOwner := db.UUID(owner)
	sessUUID, okSess := db.UUID(sessionID)
	turnUUID, okTurn := db.UUID(turnID)
	if !okOwner || !okSess || !okTurn {
		return false, nil
	}
	var exists bool
	err := s.Pool.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM qa_turns t
			JOIN qa_sessions s ON s.id = t.session_id
			WHERE t.id = $1 AND t.session_id = $2 AND s.owner_id = $3
		)`, turnUUID, sessUUID, ownerUUID).Scan(&exists)
	return exists, err
}

func uuidSliceOrEmpty(ids []string) []pgtype.UUID {
	out, ok := db.UUIDs(ids)
	if !ok || out == nil {
		return []pgtype.UUID{}
	}
	return out
}

func nullableUUID(s string) any {
	if u, ok := db.UUID(s); ok {
		return u
	}
	return nil
}

func autoSessionTitle(current, question string) string {
	if current != "新会话" {
		return current
	}
	rs := []rune(strings.TrimSpace(question))
	if len(rs) > 40 {
		return string(rs[:40])
	}
	if len(rs) == 0 {
		return current
	}
	return string(rs)
}
