package jobs

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
	"yanzhi/server/internal/platform/db"
)

func openJobsPG(t *testing.T) *pgxpool.Pool {
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

func seedJobDoc(t *testing.T, pool *pgxpool.Pool) (owner, kbID, docID string) {
	t.Helper()
	ctx := context.Background()
	owner = uuid.NewString()
	kbID = uuid.NewString()
	docID = uuid.NewString()
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = pool.Exec(bg, `DELETE FROM jobs WHERE owner_id = $1::uuid`, db.MustUUID(owner))
		_, _ = pool.Exec(bg, `DELETE FROM documents WHERE owner_id = $1::uuid`, db.MustUUID(owner))
		_, _ = pool.Exec(bg, `DELETE FROM knowledge_bases WHERE owner_id = $1::uuid`, db.MustUUID(owner))
		_, _ = pool.Exec(bg, `DELETE FROM users WHERE id = $1::uuid`, db.MustUUID(owner))
	})
	if _, err := pool.Exec(ctx, `
		INSERT INTO users (id, email, name, password_hash, role, created_at)
		VALUES ($1::uuid, $2, 'jobs-tester', 'x', 'user', now())`,
		db.MustUUID(owner), owner+"@example.edu.cn"); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO knowledge_bases (id, owner_id, name, created_at, updated_at)
		VALUES ($1::uuid, $2::uuid, 'jobs-kb', now(), now())`,
		db.MustUUID(kbID), db.MustUUID(owner)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO documents (id, owner_id, kb_id, source_type, title, status, created_at, updated_at)
		VALUES ($1::uuid, $2::uuid, $3::uuid, 'pdf', 'jobs-doc', 'pending', now(), now())`,
		db.MustUUID(docID), db.MustUUID(owner), db.MustUUID(kbID)); err != nil {
		t.Fatal(err)
	}
	return owner, kbID, docID
}

func TestJobsLifecycleAndNFR08Recovery(t *testing.T) {
	pool := openJobsPG(t)
	ctx := context.Background()
	owner, _, docID := seedJobDoc(t, pool)
	reqID := uuid.NewString()

	// 1. Enqueue + duplicate idempotency
	if err := Enqueue(ctx, pool, "ingest", owner, docID, reqID, 10, 1); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if err := Enqueue(ctx, pool, "ingest", owner, docID, reqID, 10, 1); err != nil {
		t.Fatalf("Enqueue duplicate should be ignored: %v", err)
	}

	// 2. InsertTx with savepoint duplicate handling
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := InsertTx(ctx, tx, "ingest", owner, docID, reqID, 10, 1); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("InsertTx duplicate: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	// 3. Claim ingest -> parsing, Heartbeat, Stage, Recover (NFR-08!)
	job, err := Claim(ctx, pool, []string{"ingest"})
	if err != nil || job == nil || job.TargetID != docID {
		t.Fatalf("Claim ingest: job=%+v err=%v", job, err)
	}
	Heartbeat(ctx, pool, job.ID)
	if err := Stage(ctx, pool, job.ID, "parse", "running"); err != nil {
		t.Fatalf("Stage running: %v", err)
	}
	if err := Stage(ctx, pool, job.ID, "parse", "succeeded"); err != nil {
		t.Fatalf("Stage succeeded: %v", err)
	}

	// NFR-08: Simulate server restart while parsing
	if err := Recover(ctx, pool); err != nil {
		t.Fatalf("Recover: %v", err)
	}
	var docStatus string
	var failReason *string
	var manualRetries int
	if err := pool.QueryRow(ctx, `
		SELECT status, failure_reason, manual_retry_count FROM documents WHERE id = $1::uuid`,
		db.MustUUID(docID)).Scan(&docStatus, &failReason, &manualRetries); err != nil {
		t.Fatal(err)
	}
	if docStatus != "failed" || failReason == nil || *failReason != apperr.MsgInterrupted || manualRetries != 0 {
		t.Fatalf("Recover mismatch: status=%s reason=%v manualRetries=%d", docStatus, failReason, manualRetries)
	}

	// 4. MarkInterrupted for both ingest and graph_extract, plus Succeed / Fail / Cancel
	if _, err := pool.Exec(ctx, `UPDATE documents SET status = 'ready', graph_status = 'extracting' WHERE id = $1::uuid`, db.MustUUID(docID)); err != nil {
		t.Fatal(err)
	}
	if err := Enqueue(ctx, pool, "graph_extract", owner, docID, uuid.NewString(), 80, 1); err != nil {
		t.Fatal(err)
	}
	gJob, err := Claim(ctx, pool, []string{"graph_extract"})
	if err != nil || gJob == nil {
		t.Fatalf("Claim graph_extract: job=%+v err=%v", gJob, err)
	}
	MarkInterrupted(ctx, pool, docID, "graph_extract")
	var gStatus string
	var gManual int
	if err := pool.QueryRow(ctx, `SELECT graph_status, graph_manual_retry_count FROM documents WHERE id = $1::uuid`, db.MustUUID(docID)).Scan(&gStatus, &gManual); err != nil {
		t.Fatal(err)
	}
	if gStatus != "failed" || gManual != 0 {
		t.Fatalf("graph MarkInterrupted mismatch: status=%s manual=%d", gStatus, gManual)
	}
	if err := Cancel(ctx, pool, gJob.ID); err != nil {
		t.Fatal(err)
	}

	// 5. Claim reembed + Succeed + Fail + cancelled doc + unknown kind
	if err := Enqueue(ctx, pool, "reembed", owner, docID, uuid.NewString(), 10, 1); err != nil {
		t.Fatal(err)
	}
	rJob, err := Claim(ctx, pool, []string{"reembed"})
	if err != nil || rJob == nil {
		t.Fatalf("Claim reembed: job=%+v err=%v", rJob, err)
	}
	if err := Succeed(ctx, pool, rJob.ID); err != nil {
		t.Fatal(err)
	}

	if err := Enqueue(ctx, pool, "unknown_kind", owner, docID, uuid.NewString(), 10, 1); err != nil {
		t.Fatal(err)
	}
	uJob, err := Claim(ctx, pool, []string{"unknown_kind"})
	if err != nil || uJob != nil {
		t.Fatalf("unknown kind should cancel & return nil, got job=%+v err=%v", uJob, err)
	}

	// Cancelled document returns nil on Claim
	if _, err := pool.Exec(ctx, `UPDATE documents SET cancel_requested = true WHERE id = $1::uuid`, db.MustUUID(docID)); err != nil {
		t.Fatal(err)
	}
	if err := Enqueue(ctx, pool, "ingest", owner, docID, uuid.NewString(), 10, 1); err != nil {
		t.Fatal(err)
	}
	cJob, err := Claim(ctx, pool, []string{"ingest"})
	if err != nil || cJob != nil {
		t.Fatalf("cancelled doc should return nil on Claim, got %+v err=%v", cJob, err)
	}

	// 6. Loop handles success, error, ErrCancelled, and panic
	if _, err := pool.Exec(ctx, `UPDATE documents SET cancel_requested = false WHERE id = $1::uuid`, db.MustUUID(docID)); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		fn   func(context.Context, Job) error
	}{
		{"ok", func(context.Context, Job) error { return nil }},
		{"err", func(context.Context, Job) error { return errors.New("boom") }},
		{"cancel", func(context.Context, Job) error { return ErrCancelled }},
		{"panic", func(context.Context, Job) error { panic("worker panic") }},
	} {
		if err := Enqueue(ctx, pool, "ingest", owner, docID, uuid.NewString(), 10, 1); err != nil {
			t.Fatalf("Enqueue %s: %v", tc.name, err)
		}
		loopCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		done := make(chan struct{})
		go func() {
			defer close(done)
			Loop(loopCtx, pool, []string{"ingest"}, func(c context.Context, j Job) error {
				defer cancel()
				return tc.fn(c, j)
			})
		}()
		<-done
		cancel()
	}
	// MarkInterrupted for ingest when status is parsing
	_, _ = pool.Exec(ctx, `UPDATE documents SET status = 'parsing' WHERE id = $1::uuid`, db.MustUUID(docID))
	MarkInterrupted(ctx, pool, docID, "ingest")
	if err := Fail(ctx, pool, job.ID, "manual-fail"); err != nil {
		t.Fatal(err)
	}
}
