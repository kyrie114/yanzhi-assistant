package qa_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"yanzhi/server/internal/apperr"
	"yanzhi/server/internal/knowledge"
	"yanzhi/server/internal/platform/db"
	"yanzhi/server/internal/qa"
)

func openQAPG(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL is required")
	}
	ctx := context.Background()
	migrations := os.Getenv("MIGRATIONS_DIR")
	if migrations == "" {
		migrations = filepath.Join("..", "..", "migrations")
	}
	if err := db.Migrate(ctx, url, migrations); err != nil {
		t.Fatal(err)
	}
	pool, err := db.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func TestPGStoreSessionTurnsAndCitations(t *testing.T) {
	pool := openQAPG(t)
	ctx := context.Background()
	store := qa.NewPGStore(pool)

	owner := uuid.NewString()
	other := uuid.NewString()
	kbID := uuid.NewString()
	docID := uuid.NewString()
	chunkID := uuid.NewString()

	t.Cleanup(func() {
		bg := context.Background()
		uids, _ := db.UUIDs([]string{owner, other})
		_, _ = pool.Exec(bg, `DELETE FROM qa_sessions WHERE owner_id = ANY($1::uuid[])`, uids)
		_, _ = pool.Exec(bg, `DELETE FROM documents WHERE owner_id = ANY($1::uuid[])`, uids)
		_, _ = pool.Exec(bg, `DELETE FROM knowledge_bases WHERE owner_id = ANY($1::uuid[])`, uids)
		_, _ = pool.Exec(bg, `DELETE FROM users WHERE id = ANY($1::uuid[])`, uids)
	})

	_, err := pool.Exec(ctx, `
		INSERT INTO users (id, email, name, password_hash, role, created_at)
		VALUES ($1::uuid, $2, 'qa-a', 'x', 'user', now()),
		       ($3::uuid, $4, 'qa-b', 'x', 'user', now())`,
		owner, owner+"@example.edu.cn", other, other+"@example.edu.cn")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = pool.Exec(ctx, `INSERT INTO knowledge_bases (id, owner_id, name, created_at, updated_at) VALUES ($1::uuid, $2::uuid, 'qa-kb', now(), now())`, kbID, owner)
	_, _ = pool.Exec(ctx, `INSERT INTO documents (id, owner_id, kb_id, source_type, title, status, created_at, updated_at) VALUES ($1::uuid, $2::uuid, $3::uuid, 'pdf', 'QADoc', 'ready', now(), now())`, docID, owner, kbID)
	_, _ = pool.Exec(ctx, `INSERT INTO chunks (id, document_id, kind, page_start, paragraph_index, overlap_prefix, text, version_no, char_count, term_count, embedding_generation, created_at, updated_at) VALUES ($1::uuid, $2::uuid, 'paragraph', 3, 0, '', 'chunk text', 1, 10, 2, 1, now(), now())`, chunkID, docID)

	sess, err := store.CreateSession(ctx, owner, "")
	if err != nil || sess.Title != "新会话" {
		t.Fatalf("CreateSession: %+v %v", sess, err)
	}
	if _, err := store.CreateSession(ctx, owner, string(make([]rune, 81))); err == nil {
		t.Fatal("expected long title validation error")
	}
	list, total, err := store.ListSessions(ctx, owner, 1, 20)
	if err != nil || total != 1 || len(list) != 1 {
		t.Fatalf("ListSessions: len=%d total=%d err=%v", len(list), total, err)
	}
	title, err := store.EnsureSessionOwner(ctx, owner, sess.ID)
	if err != nil || title != "新会话" {
		t.Fatalf("EnsureSessionOwner: %q %v", title, err)
	}
	if _, err := store.EnsureSessionOwner(ctx, other, sess.ID); !errors.Is(err, apperr.ErrNotFound) {
		t.Fatalf("expected ErrNotFound for other user, got %v", err)
	}

	turnID := uuid.NewString()
	err = store.SaveTurn(ctx, sess.Title, qa.PersistTurn{
		ID:            turnID,
		SessionID:     sess.ID,
		Question:      "What is LoRA?",
		Answer:        "Low-rank adaptation [c1]",
		AnswerKind:    "kb",
		Scope:         knowledge.ScopeInput{Type: "kb", KBID: kbID},
		CandidateIDs:  []string{chunkID},
		GraphExtraIDs: []string{},
		Citations: []qa.PersistCitation{{
			ChunkID:       chunkID,
			DocumentID:    docID,
			TitleSnapshot: "QADoc",
			PageStart:     3,
			Origin:        "kb",
			Label:         "QADoc, 第 3 页",
		}},
		CreatedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("SaveTurn: %v", err)
	}

	if ok, err := store.TurnExists(ctx, owner, sess.ID, turnID); err != nil || !ok {
		t.Fatalf("TurnExists got %v %v", ok, err)
	}
	hist, err := store.RecentHistory(ctx, owner, sess.ID, 5)
	if err != nil || len(hist) != 1 || hist[0].Question != "What is LoRA?" {
		t.Fatalf("RecentHistory got %+v err=%v", hist, err)
	}

	detail, err := store.GetSession(ctx, owner, sess.ID)
	if err != nil || len(detail.Turns) != 1 || len(detail.Turns[0].Citations) != 1 || detail.Turns[0].Citations[0].Label != "QADoc, 第 3 页" {
		t.Fatalf("GetSession got %+v err=%v", detail, err)
	}

	// Deleting doc normalizes citation label to 来源已删除
	_, _ = pool.Exec(ctx, `DELETE FROM documents WHERE id = $1::uuid`, docID)
	detailAfterDelete, err := store.GetSession(ctx, owner, sess.ID)
	if err != nil || detailAfterDelete.Turns[0].Citations[0].Label != "来源已删除" {
		t.Fatalf("expected 来源已删除 after doc delete, got %+v err=%v", detailAfterDelete, err)
	}

	patched, err := store.PatchSession(ctx, owner, sess.ID, "重命名会话")
	if err != nil || patched.Title != "重命名会话" {
		t.Fatalf("PatchSession got %+v err=%v", patched, err)
	}
	if err := store.DeleteSession(ctx, owner, sess.ID); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	if _, err := store.GetSession(ctx, owner, sess.ID); !errors.Is(err, apperr.ErrNotFound) {
		t.Fatalf("expected ErrNotFound after delete, got %v", err)
	}
}
