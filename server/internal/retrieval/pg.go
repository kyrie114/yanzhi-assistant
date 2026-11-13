package retrieval

import (
	"context"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"yanzhi/server/internal/platform/db"
)

// PGCorpus 复用 VectorSQL/KeywordSQL 与 pgxpool 执行检索及父块水合。
type PGCorpus struct {
	Pool *pgxpool.Pool
}

func NewPGCorpus(pool *pgxpool.Pool) *PGCorpus {
	return &PGCorpus{Pool: pool}
}

func (c *PGCorpus) VectorRecall(ctx context.Context, scope Scope, vec []float32, gen int64, threshold float64, limit int) ([]string, error) {
	ownerUUID, ok := db.UUID(scope.OwnerID())
	if !ok {
		return nil, nil
	}
	docUUIDs, ok := db.UUIDs(scope.DocIDs())
	if !ok || len(docUUIDs) == 0 {
		return nil, nil
	}
	ids, err := vectorHits(ctx, c.Pool, ownerUUID, docUUIDs, vec, gen, threshold)
	if err != nil {
		return nil, err
	}
	if limit > 0 && len(ids) > limit {
		ids = ids[:limit]
	}
	return ids, nil
}

func (c *PGCorpus) KeywordRecall(ctx context.Context, scope Scope, query string, k1, b float64, limit int) ([]string, error) {
	ownerUUID, ok := db.UUID(scope.OwnerID())
	if !ok {
		return nil, nil
	}
	docUUIDs, ok := db.UUIDs(scope.DocIDs())
	if !ok || len(docUUIDs) == 0 {
		return nil, nil
	}
	ids, err := keywordHits(ctx, c.Pool, ownerUUID, docUUIDs, query, k1, b)
	if err != nil {
		return nil, err
	}
	if limit > 0 && len(ids) > limit {
		ids = ids[:limit]
	}
	return ids, nil
}

func (c *PGCorpus) LoadChunks(ctx context.Context, scope Scope, chunkIDs []string) ([]Hit, error) {
	if len(chunkIDs) == 0 {
		return nil, nil
	}
	ownerUUID, ok := db.UUID(scope.OwnerID())
	if !ok {
		return nil, nil
	}
	docUUIDs, ok := db.UUIDs(scope.DocIDs())
	if !ok || len(docUUIDs) == 0 {
		return nil, nil
	}
	cUUIDs, ok := db.UUIDs(chunkIDs)
	if !ok {
		return nil, nil
	}
	rows, err := c.Pool.Query(ctx, `
		SELECT c.id::text, d.id::text, d.title, c.page_start, c.text, p.text
		FROM chunks c
		JOIN documents d ON d.id = c.document_id
		LEFT JOIN parent_chunks p ON p.id = c.parent_id
		WHERE c.id = ANY($1::uuid[])
		  AND d.owner_id = $2
		  AND d.status = 'ready'
		  AND d.id = ANY($3::uuid[])`, cUUIDs, ownerUUID, docUUIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	byID := make(map[string]Hit, len(chunkIDs))
	for rows.Next() {
		var h Hit
		var parent pgtype.Text
		if err := rows.Scan(&h.ChunkID, &h.DocumentID, &h.Title, &h.PageStart, &h.Text, &parent); err != nil {
			return nil, err
		}
		if parent.Valid {
			h.ParentText = parent.String
		}
		byID[h.ChunkID] = h
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := make([]Hit, 0, len(chunkIDs))
	seen := make(map[string]struct{}, len(chunkIDs))
	for _, cid := range chunkIDs {
		if _, dup := seen[cid]; dup {
			continue
		}
		seen[cid] = struct{}{}
		if h, ok := byID[cid]; ok {
			out = append(out, h)
		}
	}
	return out, nil
}
