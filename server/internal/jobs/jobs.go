package jobs

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"yanzhi/server/internal/apperr"
	"yanzhi/server/internal/platform/db"
	"yanzhi/server/internal/retrieval"
)

type Job struct {
	ID, Kind, OwnerID, TargetID, RequestID string
	Attempt                                int
}

var ErrCancelled = errors.New("cancelled")

func Enqueue(ctx context.Context, pool *pgxpool.Pool, kind, owner, target, request string, priority, attempt int) error {
	if attempt < 1 {
		attempt = 1
	}
	_, err := pool.Exec(ctx, `
		INSERT INTO jobs (id, kind, owner_id, target_id, request_id, priority, attempt, status, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, 'pending', now())`,
		db.MustUUID(db.NewID()), kind, db.MustUUID(owner), db.MustUUID(target), db.MustUUID(request), priority, attempt)
	var pe *pgconn.PgError
	if errors.As(err, &pe) && pe.Code == "23505" {
		return nil
	}
	return err
}

func Claim(ctx context.Context, pool *pgxpool.Pool, kinds []string) (*Job, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	var job Job
	err = tx.QueryRow(ctx, `
		UPDATE jobs SET status = 'running', started_at = COALESCE(started_at, now()), heartbeat_at = now()
		WHERE id = (
			SELECT id FROM jobs
			WHERE status = 'pending' AND kind = ANY($1::text[])
			ORDER BY priority, created_at
			FOR UPDATE SKIP LOCKED
			LIMIT 1
		)
		RETURNING id::text, kind, COALESCE(owner_id::text, ''), target_id::text, request_id::text, attempt`,
		kinds).Scan(&job.ID, &job.Kind, &job.OwnerID, &job.TargetID, &job.RequestID, &job.Attempt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var tag pgconn.CommandTag
	switch job.Kind {
	case "ingest":
		tag, err = tx.Exec(ctx, `
			UPDATE documents SET status = 'parsing', updated_at = now()
			WHERE id = $1 AND cancel_requested = false`, db.MustUUID(job.TargetID))
	case "reembed":
		tag, err = tx.Exec(ctx, `
			UPDATE documents SET status = 'embedding', updated_at = now()
			WHERE id = $1 AND cancel_requested = false`, db.MustUUID(job.TargetID))
	case "graph_extract":
		tag, err = tx.Exec(ctx, `
			UPDATE documents SET graph_status = 'extracting', updated_at = now()
			WHERE id = $1 AND cancel_requested = false AND status = 'ready'`, db.MustUUID(job.TargetID))
	default:
		tag, err = tx.Exec(ctx, `UPDATE jobs SET status = 'cancelled', finished_at = now() WHERE id = $1`, db.MustUUID(job.ID))
		if err != nil {
			return nil, err
		}
		return nil, tx.Commit(ctx)
	}
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		_, err = tx.Exec(ctx, `
			UPDATE jobs SET status = 'cancelled', finished_at = now(), error_message = '已取消'
			WHERE id = $1`, db.MustUUID(job.ID))
		if err != nil {
			return nil, err
		}
		if err := tx.Commit(ctx); err != nil {
			return nil, err
		}
		return nil, nil
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &job, nil
}

func Succeed(ctx context.Context, pool *pgxpool.Pool, id string) error {
	_, err := pool.Exec(ctx, `UPDATE jobs SET status = 'succeeded', finished_at = now(), heartbeat_at = now() WHERE id = $1`, db.MustUUID(id))
	return err
}

func Fail(ctx context.Context, pool *pgxpool.Pool, id, message string) error {
	_, err := pool.Exec(ctx, `
		UPDATE jobs SET status = 'failed', error_message = $2, finished_at = now() WHERE id = $1`,
		db.MustUUID(id), message)
	return err
}

func Cancel(ctx context.Context, pool *pgxpool.Pool, id string) error {
	_, err := pool.Exec(ctx, `
		UPDATE jobs SET status = 'cancelled', finished_at = now() WHERE id = $1 AND status = 'running'`,
		db.MustUUID(id))
	return err
}

func Heartbeat(ctx context.Context, pool *pgxpool.Pool, id string) {
	_, _ = pool.Exec(ctx, `UPDATE jobs SET heartbeat_at = now() WHERE id = $1`, db.MustUUID(id))
}

func Stage(ctx context.Context, pool *pgxpool.Pool, jobID, name, status string) error {
	_, err := pool.Exec(ctx, `
		INSERT INTO job_stages (id, job_id, name, status, started_at, finished_at)
		VALUES ($1, $2, $3, $4, now(), CASE WHEN $4 IN ('succeeded', 'failed', 'skipped') THEN now() ELSE NULL END)
		ON CONFLICT (job_id, name) DO UPDATE
		SET status = EXCLUDED.status,
		    finished_at = CASE WHEN EXCLUDED.status IN ('succeeded', 'failed', 'skipped') THEN now() ELSE job_stages.finished_at END`,
		db.MustUUID(db.NewID()), db.MustUUID(jobID), name, status)
	return err
}

func Recover(ctx context.Context, pool *pgxpool.Pool) error {
	for _, part := range strings.Split(retrieval.RecoverSQL, ";") {
		stmt := strings.TrimSpace(part)
		if stmt == "" {
			continue
		}
		if _, err := pool.Exec(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}

func MarkInterrupted(ctx context.Context, pool *pgxpool.Pool, target, kind string) {
	if kind == "graph_extract" {
		_, _ = pool.Exec(ctx, `
			UPDATE documents SET graph_status = 'failed', graph_failure_reason = $2, updated_at = now()
			WHERE id = $1 AND graph_status = 'extracting'`, db.MustUUID(target), apperr.MsgInterrupted)
		return
	}
	_, _ = pool.Exec(ctx, `
		UPDATE documents SET status = 'failed', failure_reason = $2, updated_at = now()
		WHERE id = $1 AND status IN ('parsing', 'embedding')`, db.MustUUID(target), apperr.MsgInterrupted)
}

func Loop(ctx context.Context, pool *pgxpool.Pool, kinds []string, handle func(context.Context, Job) error) {
	for {
		if ctx.Err() != nil {
			return
		}
		job, err := Claim(ctx, pool, kinds)
		if err != nil || job == nil {
			select {
			case <-ctx.Done():
				return
			case <-time.After(400 * time.Millisecond):
			}
			continue
		}
		func() {
			defer func() {
				if rec := recover(); rec != nil {
					_ = Fail(ctx, pool, job.ID, apperr.MsgInterrupted)
					MarkInterrupted(ctx, pool, job.TargetID, job.Kind)
				}
			}()
			err := handle(ctx, *job)
			if errors.Is(err, ErrCancelled) {
				_ = Cancel(ctx, pool, job.ID)
				return
			}
			if err != nil {
				_ = Fail(ctx, pool, job.ID, err.Error())
				return
			}
			_ = Succeed(ctx, pool, job.ID)
		}()
	}
}
