package ingest

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"yanzhi/server/internal/apperr"
	"yanzhi/server/internal/jobs"
	"yanzhi/server/internal/platform/db"
	"yanzhi/server/internal/platform/docreader"
	"yanzhi/server/internal/platform/files"
	"yanzhi/server/internal/provider"
)

func TestIngestUnitHelpersAndEmbedRetry(t *testing.T) {
	if !hasText([]Page{{PageNo: 1, Blocks: []Block{{Text: "hello"}}}}) {
		t.Fatal("expected hasText true")
	}
	if hasText([]Page{{PageNo: 1, Blocks: []Block{{Text: "   "}}}}) {
		t.Fatal("expected hasText false for blank text")
	}
	if reasonForParse("ENCRYPTED") != apperr.MsgEncrypted || reasonForParse("TOO_MANY_PAGES") != apperr.ErrTooManyPages.Message || reasonForParse("OTHER") != apperr.MsgCorrupted {
		t.Fatal("unexpected reasonForParse mapping")
	}
	if (Runner{}).timeout() != 60*time.Second || (Runner{Timeout: 5 * time.Second}).timeout() != 5*time.Second {
		t.Fatal("unexpected Runner.timeout")
	}

	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[{"index":0,"embedding":[0.1,0.2,0.3,0.4]}],"usage":{"prompt_tokens":3}}`))
	}))
	defer srv.Close()

	runner := Runner{
		AI:      provider.Client{HTTP: srv.Client()},
		Timeout: 2 * time.Second,
	}
	vecs, err := runner.embedRetry(context.Background(), jobs.Job{RequestID: uuid.NewString(), OwnerID: uuid.NewString()},
		provider.Remote{Kind: "embedding", BaseURL: srv.URL, Model: "embed-test", Dimension: 4},
		[]string{"hello"},
	)
	if err != nil || len(vecs) != 1 || calls.Load() != 3 {
		t.Fatalf("expected 3 calls (2 retries) then succeed: calls=%d vecs=%v err=%v", calls.Load(), vecs, err)
	}
}

func openIngestPG(t *testing.T) *pgxpool.Pool {
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

func seedIngestFixtures(t *testing.T, pool *pgxpool.Pool, store files.Store, embedURL string) (owner, kbID, docID, key string) {
	t.Helper()
	ctx := context.Background()
	owner = uuid.NewString()
	kbID = uuid.NewString()
	docID = uuid.NewString()
	key = filepath.Join(owner, docID+".pdf")
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = pool.Exec(bg, `DELETE FROM model_call_logs WHERE user_id = $1::uuid`, db.MustUUID(owner))
		_, _ = pool.Exec(bg, `DELETE FROM jobs WHERE owner_id = $1::uuid`, db.MustUUID(owner))
		_, _ = pool.Exec(bg, `DELETE FROM documents WHERE owner_id = $1::uuid`, db.MustUUID(owner))
		_, _ = pool.Exec(bg, `DELETE FROM knowledge_bases WHERE owner_id = $1::uuid`, db.MustUUID(owner))
		_, _ = pool.Exec(bg, `DELETE FROM users WHERE id = $1::uuid`, db.MustUUID(owner))
	})

	if _, err := pool.Exec(ctx, `
		INSERT INTO users (id, email, name, password_hash, role, created_at)
		VALUES ($1::uuid, $2, 'ingest-tester', 'x', 'user', now())`,
		db.MustUUID(owner), owner+"@example.edu.cn"); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO knowledge_bases (id, owner_id, name, graph_enabled, created_at, updated_at)
		VALUES ($1::uuid, $2::uuid, 'ingest-kb', true, now(), now())`,
		db.MustUUID(kbID), db.MustUUID(owner)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO documents (id, owner_id, kb_id, source_type, title, storage_key, status, created_at, updated_at)
		VALUES ($1::uuid, $2::uuid, $3::uuid, 'pdf', 'ingest-doc', $4, 'pending', now(), now())`,
		db.MustUUID(docID), db.MustUUID(owner), db.MustUUID(kbID), key); err != nil {
		t.Fatal(err)
	}
	if err := store.Put(key, []byte("%PDF-1.4\n1 0 obj\n<< /Type /Page >>\nendobj\n")); err != nil {
		t.Fatal(err)
	}
	models := provider.Store{Pool: pool, Key: []byte("0123456789abcdef0123456789abcdef")}
	dim := 4
	apiKey := "sk-ingest-test"
	if _, err := models.Put(ctx, "embedding", provider.PutInput{
		BaseURL: embedURL, ModelName: "embed-test", APIKey: &apiKey, Dimension: &dim, ConfirmReindex: true,
	}); err != nil {
		t.Fatal(err)
	}
	return owner, kbID, docID, key
}

func TestIngestFaultInjectionAndReembedPG(t *testing.T) {
	pool := openIngestPG(t)
	ctx := context.Background()
	store := files.Store{Root: t.TempDir()}

	var embedFail atomic.Bool
	var embedDim atomic.Int32
	embedDim.Store(4)
	embedSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if embedFail.Load() {
			w.WriteHeader(http.StatusGatewayTimeout)
			return
		}
		vec := make([]float32, embedDim.Load())
		for i := range vec {
			vec[i] = 0.25
		}
		_, _ = w.Write([]byte(`{"data":[{"index":0,"embedding":[0.25,0.25,0.25,0.25]}],"usage":{"prompt_tokens":5}}`))
	}))
	defer embedSrv.Close()

	owner, _, docID, key := seedIngestFixtures(t, pool, store, embedSrv.URL)
	models := provider.Store{Pool: pool, Key: []byte("0123456789abcdef0123456789abcdef")}

	// 1. Docreader unavailable -> failed with MsgDocreader
	reqID := uuid.NewString()
	_ = jobs.Enqueue(ctx, pool, "ingest", owner, docID, reqID, 10, 1)
	job, err := jobs.Claim(ctx, pool, []string{"ingest"})
	if err != nil || job == nil {
		t.Fatalf("claim: %v %+v", err, job)
	}
	runner := Runner{
		Pool: pool, Files: store,
		Reader: docreader.New("127.0.0.1:1", "tok", 200*time.Millisecond),
		Models: models, AI: provider.Client{HTTP: embedSrv.Client()},
		Chunk: DefaultConfig(), EmbedBatch: 8, Timeout: 2 * time.Second,
	}
	if err := runner.Run(ctx, *job); err == nil {
		t.Fatal("expected docreader failure")
	}
	var st, reason string
	_ = pool.QueryRow(ctx, `SELECT status, failure_reason FROM documents WHERE id = $1::uuid`, db.MustUUID(docID)).Scan(&st, &reason)
	if st != "failed" || reason != apperr.MsgDocreader {
		t.Fatalf("docreader fail got status=%s reason=%s", st, reason)
	}

	// 2. Missing file -> failed with MsgCorrupted
	_ = store.Delete(key)
	_ = jobs.Enqueue(ctx, pool, "ingest", owner, docID, uuid.NewString(), 10, 1)
	job2, _ := jobs.Claim(ctx, pool, []string{"ingest"})
	if err := runner.Run(ctx, *job2); err == nil {
		t.Fatal("expected missing file failure")
	}

	// 3. Direct storeChunks + embed failure (3 retries -> MsgEmbedDown) + Reembed recovery
	_ = jobs.Enqueue(ctx, pool, "reembed", owner, docID, uuid.NewString(), 10, 1)
	job3, _ := jobs.Claim(ctx, pool, []string{"reembed"})
	gen, _ := models.ActiveGeneration(ctx)
	chunks, parents := Pack([]Page{{PageNo: 1, Blocks: []Block{{Kind: "paragraph", Text: "LoRA fine tuning method"}}}}, DefaultConfig())
	if err := runner.storeChunks(ctx, *job3, docreader.Result{PageCount: 1, HasPhysicalPages: true}, chunks, parents, gen); err != nil {
		t.Fatalf("storeChunks: %v", err)
	}

	embedFail.Store(true)
	if err := runner.Reembed(ctx, *job3); err == nil {
		t.Fatal("expected embed failure")
	}
	_ = pool.QueryRow(ctx, `SELECT status, failure_reason FROM documents WHERE id = $1::uuid`, db.MustUUID(docID)).Scan(&st, &reason)
	if st != "failed" || reason != apperr.MsgEmbedDown {
		t.Fatalf("embed fail got status=%s reason=%s", st, reason)
	}

	// 4. Reembed succeeds & records model_call_logs
	embedFail.Store(false)
	_ = jobs.Enqueue(ctx, pool, "reembed", owner, docID, uuid.NewString(), 10, 1)
	job4, _ := jobs.Claim(ctx, pool, []string{"reembed"})
	if err := runner.Reembed(ctx, *job4); err != nil {
		t.Fatalf("Reembed recover failed: %v", err)
	}
	_ = pool.QueryRow(ctx, `SELECT status FROM documents WHERE id = $1::uuid`, db.MustUUID(docID)).Scan(&st)
	if st != "ready" {
		t.Fatalf("expected ready after reembed, got %s", st)
	}
	var logCount int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM model_call_logs WHERE user_id = $1::uuid AND kind = 'embedding'`, db.MustUUID(owner)).Scan(&logCount)
	if logCount < 4 {
		t.Fatalf("expected embedding model_call_logs recorded, got %d", logCount)
	}

	if alive, err := Alive(ctx, pool, docID); err != nil || !alive {
		t.Fatalf("Alive got %v %v", alive, err)
	}
	tx, _ := pool.Begin(ctx)
	if err := DeleteDerived(ctx, tx, owner, docID); err != nil {
		t.Fatalf("DeleteDerived: %v", err)
	}
	_ = tx.Commit(ctx)
}
