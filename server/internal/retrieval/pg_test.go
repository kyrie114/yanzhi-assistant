package retrieval_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pgvector/pgvector-go"

	"yanzhi/server/internal/platform/db"
	"yanzhi/server/internal/retrieval"
	"yanzhi/server/internal/textutil"
)

type pgScope struct {
	owner string
	docs  []string
}

func (s pgScope) OwnerID() string                      { return s.owner }
func (s pgScope) DocIDs() []string                     { return s.docs }
func (s pgScope) ThresholdOr(fallback float64) float64 { return fallback }

func openRetrievalPG(t *testing.T) *pgxpool.Pool {
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

func TestPGCorpusVectorKeywordAndHydration(t *testing.T) {
	pool := openRetrievalPG(t)
	ctx := context.Background()

	owner := uuid.NewString()
	other := uuid.NewString()
	kbID := uuid.NewString()
	docReady := uuid.NewString()
	docPending := uuid.NewString()
	parentID := uuid.NewString()
	chunkCurr := uuid.NewString()
	chunkStale := uuid.NewString()
	chunkPending := uuid.NewString()

	t.Cleanup(func() {
		bg := context.Background()
		uids, _ := db.UUIDs([]string{owner, other})
		_, _ = pool.Exec(bg, `DELETE FROM documents WHERE owner_id = ANY($1::uuid[])`, uids)
		_, _ = pool.Exec(bg, `DELETE FROM knowledge_bases WHERE owner_id = ANY($1::uuid[])`, uids)
		_, _ = pool.Exec(bg, `DELETE FROM term_stats WHERE owner_id = ANY($1::uuid[])`, uids)
		_, _ = pool.Exec(bg, `DELETE FROM users WHERE id = ANY($1::uuid[])`, uids)
	})

	_, err := pool.Exec(ctx, `
		INSERT INTO users (id, email, name, password_hash, role, created_at)
		VALUES ($1::uuid, $2, 'ret-a', 'x', 'user', now()),
		       ($3::uuid, $4, 'ret-b', 'x', 'user', now())`,
		owner, owner+"@example.edu.cn", other, other+"@example.edu.cn")
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `
		INSERT INTO knowledge_bases (id, owner_id, name, created_at, updated_at)
		VALUES ($1::uuid, $2::uuid, 'ret-kb', now(), now())`, kbID, owner)
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `
		INSERT INTO documents (id, owner_id, kb_id, source_type, title, status, embedding_generation, created_at, updated_at)
		VALUES ($1::uuid, $2::uuid, $3::uuid, 'pdf', 'ReadyDoc', 'ready', 2, now(), now()),
		       ($4::uuid, $2::uuid, $3::uuid, 'pdf', 'PendingDoc', 'pending', 2, now(), now())`,
		docReady, owner, kbID, docPending)
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `
		INSERT INTO parent_chunks (id, document_id, position, page_start, text)
		VALUES ($1::uuid, $2::uuid, 0, 4, 'Parent context paragraph for LoRA')`, parentID, docReady)
	if err != nil {
		t.Fatal(err)
	}
	// Temporarily alter vector dimension only if needed? 001_init.sql chunks.embedding is vector(1024) by default unless altered.
	// Query actual dimension of chunks.embedding so test works regardless of prior reindex tests!
	var vecDim int
	if err := pool.QueryRow(ctx, `
		SELECT COALESCE(NULLIF(atttypmod, -1), 1024)
		FROM pg_attribute WHERE attrelid = 'chunks'::regclass AND attname = 'embedding'`).Scan(&vecDim); err != nil {
		t.Fatal(err)
	}
	makeVec := func(v0, v1 float32) pgvector.Vector {
		arr := make([]float32, vecDim)
		arr[0] = v0
		if vecDim > 1 {
			arr[1] = v1
		}
		return pgvector.NewVector(arr)
	}

	_, err = pool.Exec(ctx, `
		INSERT INTO chunks (id, document_id, parent_id, kind, page_start, paragraph_index, overlap_prefix, text, version_no, char_count, term_count, embedding, embedding_generation, created_at, updated_at)
		VALUES ($1::uuid, $2::uuid, $3::uuid, 'paragraph', 4, 0, '', 'ZephyrBench9 exact method', 1, 24, 3, $4, 2, now(), now()),
		       ($5::uuid, $2::uuid, $3::uuid, 'paragraph', 5, 1, '', 'ZephyrBench9 stale gen', 1, 22, 3, $4, 1, now(), now()),
		       ($6::uuid, $7::uuid, NULL, 'paragraph', 1, 0, '', 'ZephyrBench9 pending doc', 1, 24, 3, $4, 2, now(), now())`,
		chunkCurr, docReady, parentID, makeVec(1, 0), chunkStale, chunkPending, docPending)
	if err != nil {
		t.Fatal(err)
	}
	for _, term := range textutil.Terms("ZephyrBench9") {
		_, _ = pool.Exec(ctx, `INSERT INTO chunk_postings (chunk_id, term, field, tf) VALUES ($1::uuid, $2, 'text', 1)`, chunkCurr, term)
		_, _ = pool.Exec(ctx, `INSERT INTO chunk_postings (chunk_id, term, field, tf) VALUES ($1::uuid, $2, 'question', 5)`, chunkStale, term)
		_, _ = pool.Exec(ctx, `INSERT INTO chunk_postings (chunk_id, term, field, tf) VALUES ($1::uuid, $2, 'text', 1)`, chunkStale, term)
		_, _ = pool.Exec(ctx, `INSERT INTO term_stats (owner_id, term, df) VALUES ($1::uuid, $2, 2) ON CONFLICT (owner_id, term) DO UPDATE SET df = 2`, owner, term)
	}

	corpus := retrieval.NewPGCorpus(pool)
	scope := pgScope{owner: owner, docs: []string{docReady, docPending}}

	qVec := make([]float32, vecDim)
	qVec[0] = 1
	vHits, err := corpus.VectorRecall(ctx, scope, qVec, 2, 0.3, 10)
	if err != nil {
		t.Fatalf("VectorRecall: %v", err)
	}
	if len(vHits) != 1 || vHits[0] != chunkCurr {
		t.Fatalf("VectorRecall expected [%s], got %v", chunkCurr, vHits)
	}

	kHits, err := corpus.KeywordRecall(ctx, scope, "ZephyrBench9", 1.2, 0.75, 10)
	if err != nil {
		t.Fatalf("KeywordRecall: %v", err)
	}
	if len(kHits) != 1 || kHits[0] != chunkCurr {
		t.Fatalf("KeywordRecall expected only current-gen text hit [%s], got %v", chunkCurr, kHits)
	}

	loaded, err := corpus.LoadChunks(ctx, scope, []string{chunkCurr, chunkCurr, chunkPending})
	if err != nil {
		t.Fatalf("LoadChunks: %v", err)
	}
	if len(loaded) != 1 || loaded[0].ChunkID != chunkCurr || loaded[0].ParentText == "" || loaded[0].PageStart != 4 {
		t.Fatalf("LoadChunks mismatch: %+v", loaded)
	}

	merged, err := retrieval.Search(ctx, pool, owner, []string{docReady}, qVec, "ZephyrBench9", 2, 0.3, 1.2, 0.75, 60, 5)
	if err != nil || len(merged) != 1 || merged[0] != chunkCurr {
		t.Fatalf("retrieval.Search got %v err=%v", merged, err)
	}

	// Cross-user scope returns empty
	foreignHits, err := corpus.VectorRecall(ctx, pgScope{owner: other, docs: []string{docReady}}, qVec, 2, 0.3, 10)
	if err != nil || len(foreignHits) != 0 {
		t.Fatalf("foreign VectorRecall leaked: %v %v", foreignHits, err)
	}
}
