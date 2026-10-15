package ingest

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"yanzhi/server/internal/platform/db"
)

type Execer interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

func DeleteDerived(ctx context.Context, q Execer, owner, documentID string) error {
	ownerID := db.MustUUID(owner)
	docID := db.MustUUID(documentID)
	if _, err := q.Exec(ctx, `
		UPDATE term_stats ts
		SET df = GREATEST(df - sub.n, 0)
		FROM (
			SELECT p.term, count(DISTINCT p.chunk_id)::int AS n
			FROM chunk_postings p
			JOIN chunks c ON c.id = p.chunk_id
			WHERE c.document_id = $2 AND p.field = 'text'
			GROUP BY p.term
		) sub
		WHERE ts.owner_id = $1 AND ts.term = sub.term`, ownerID, docID); err != nil {
		return err
	}
	if _, err := q.Exec(ctx, `DELETE FROM term_stats WHERE owner_id = $1 AND df <= 0`, ownerID); err != nil {
		return err
	}
	if _, err := q.Exec(ctx, `DELETE FROM chunks WHERE document_id = $1`, docID); err != nil {
		return err
	}
	_, err := q.Exec(ctx, `DELETE FROM parent_chunks WHERE document_id = $1`, docID)
	return err
}

func Alive(ctx context.Context, q Execer, documentID string) (bool, error) {
	var cancel bool
	err := q.QueryRow(ctx, `SELECT cancel_requested FROM documents WHERE id = $1`, db.MustUUID(documentID)).Scan(&cancel)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return !cancel, nil
}
