package reading_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"yanzhi/server/internal/apperr"
	"yanzhi/server/internal/platform/db"
	"yanzhi/server/internal/reading"
)

func openReadingPG(t *testing.T) *pgxpool.Pool {
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

func TestPGStoreRoundTripInvalidationAndCascade(t *testing.T) {
	pool := openReadingPG(t)
	ctx := context.Background()
	store := reading.NewPGStore(pool)

	owner := uuid.NewString()
	other := uuid.NewString()
	kbID := uuid.NewString()
	docID := uuid.NewString()
	parentID := uuid.NewString()
	chunkID := uuid.NewString()

	t.Cleanup(func() {
		bg := context.Background()
		uids, _ := db.UUIDs([]string{owner, other})
		_, _ = pool.Exec(bg, `DELETE FROM documents WHERE owner_id = ANY($1::uuid[])`, uids)
		_, _ = pool.Exec(bg, `DELETE FROM knowledge_bases WHERE owner_id = ANY($1::uuid[])`, uids)
		_, _ = pool.Exec(bg, `DELETE FROM users WHERE id = ANY($1::uuid[])`, uids)
	})

	if _, err := pool.Exec(ctx, `
		INSERT INTO users (id, email, name, password_hash, role, created_at)
		VALUES ($1::uuid, $2, 'read-a', 'x', 'user', now()),
		       ($3::uuid, $4, 'read-b', 'x', 'user', now())`,
		owner, owner+"@example.edu.cn", other, other+"@example.edu.cn"); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO knowledge_bases (id, owner_id, name, created_at, updated_at) VALUES ($1::uuid, $2::uuid, 'read-kb', now(), now())`, kbID, owner); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO documents (id, owner_id, kb_id, source_type, title, status, page_count, has_physical_pages, storage_key, created_at, updated_at)
		VALUES ($1::uuid, $2::uuid, $3::uuid, 'pdf', 'ReadingPGDoc', 'ready', 6, true, 'owner/doc.pdf', now(), now())`,
		docID, owner, kbID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO parent_chunks (id, document_id, position, page_start, text) VALUES ($1::uuid, $2::uuid, 1, 4, 'parent')`, parentID, docID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO chunks (id, document_id, parent_id, kind, page_start, paragraph_index, overlap_prefix, text, version_no, char_count, term_count, embedding_generation, created_at, updated_at)
		VALUES ($1::uuid, $2::uuid, $3::uuid, 'text', 4, 1, 0, 'PG Page 4 text', 1, 14, 4, 1, now(), now())`,
		chunkID, docID, parentID); err != nil {
		t.Fatal(err)
	}

	if _, err := store.GetDocMeta(ctx, other, docID); !errors.Is(err, apperr.ErrNotFound) {
		t.Fatalf("expected ErrNotFound for other owner, got %v", err)
	}
	meta, err := store.GetDocMeta(ctx, owner, docID)
	if err != nil || !meta.HasOriginalFile || meta.PageCount != 6 || meta.Title != "ReadingPGDoc" {
		t.Fatalf("unexpected GetDocMeta: %+v err=%v", meta, err)
	}

	chunks, err := store.ListPageChunks(ctx, owner, docID, 4)
	if err != nil || len(chunks) != 1 || chunks[0].Text != "PG Page 4 text" {
		t.Fatalf("unexpected chunks: %+v err=%v", chunks, err)
	}
	_, hashV1 := reading.AssemblePageText(chunks)
	if err := store.SaveTranslation(ctx, owner, docID, 4, hashV1, "PG第四页译文"); err != nil {
		t.Fatal(err)
	}
	got, hit, err := store.GetTranslation(ctx, owner, docID, 4, hashV1)
	if err != nil || !hit || got != "PG第四页译文" {
		t.Fatalf("expected PG cache hit, got=%q hit=%v err=%v", got, hit, err)
	}

	// Mismatched hash after chunk revision deletes stale row from page_translations.
	if _, hit, err := store.GetTranslation(ctx, owner, docID, 4, "different-sha256"); err != nil || hit {
		t.Fatalf("expected stale cache miss, hit=%v err=%v", hit, err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM page_translations WHERE document_id = $1::uuid`, docID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("expected stale translation row to be deleted, count=%d", count)
	}

	// Re-save and verify cascade deletion when deleting document.
	if err := store.SaveTranslation(ctx, owner, docID, 4, hashV1, "再次保存"); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM documents WHERE id = $1::uuid`, docID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM page_translations WHERE owner_id = $1::uuid`, owner).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("expected cascade delete of page_translations, count=%d", count)
	}
}
