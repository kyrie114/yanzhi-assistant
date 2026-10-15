package retrieval

import (
	"context"
	"sort"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pgvector/pgvector-go"
	"yanzhi/server/internal/platform/db"
	"yanzhi/server/internal/textutil"
)

func Search(ctx context.Context, pool *pgxpool.Pool, owner string, docIDs []string, vector []float32, query string, generation int64, threshold, k1, b float64, rrfK, topK int) ([]string, error) {
	if len(docIDs) == 0 {
		return nil, nil
	}
	ids, ok := db.UUIDs(docIDs)
	if !ok {
		return nil, nil
	}
	ownerID := db.MustUUID(owner)
	vectorIDs, err := vectorHits(ctx, pool, ownerID, ids, vector, generation, threshold)
	if err != nil {
		return nil, err
	}
	keywordIDs, err := keywordHits(ctx, pool, ownerID, ids, query, k1, b)
	if err != nil {
		return nil, err
	}
	return Merge(vectorIDs, keywordIDs, rrfK, topK), nil
}

func vectorHits(ctx context.Context, pool *pgxpool.Pool, owner pgtype.UUID, ids []pgtype.UUID, vector []float32, generation int64, threshold float64) ([]string, error) {
	if len(vector) == 0 {
		return nil, nil
	}
	rows, err := pool.Query(ctx, VectorSQL, pgvector.NewVector(vector), owner, generation, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var got []string
	var scoreList []float64
	for rows.Next() {
		var id string
		var score float64
		if err := rows.Scan(&id, &score); err != nil {
			return nil, err
		}
		got = append(got, id)
		scoreList = append(scoreList, score)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return FilterThreshold(got, scoreList, threshold), nil
}

func keywordHits(ctx context.Context, pool *pgxpool.Pool, owner pgtype.UUID, ids []pgtype.UUID, query string, k1, b float64) ([]string, error) {
	terms := textutil.Terms(query)
	if len(terms) == 0 {
		return nil, nil
	}
	var nDocs int64
	var avg float64
	if err := pool.QueryRow(ctx, `
		SELECT count(*), COALESCE(avg(c.term_count), 0)
		FROM chunks c
		JOIN documents d ON d.id = c.document_id
		WHERE d.owner_id = $1 AND d.status = 'ready'`, owner).Scan(&nDocs, &avg); err != nil {
		return nil, err
	}
	rows, err := pool.Query(ctx, KeywordSQL, owner, ids, terms)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type acc struct {
		score float64
	}
	scores := map[string]*acc{}
	for rows.Next() {
		var id, term string
		var tf, termCount, df int
		if err := rows.Scan(&id, &term, &tf, &termCount, &df); err != nil {
			return nil, err
		}
		a := scores[id]
		if a == nil {
			a = &acc{}
			scores[id] = a
		}
		a.score += textutil.BM25(float64(tf), float64(df), float64(termCount), avg, float64(nDocs), k1, b)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	type pair struct {
		id    string
		score float64
	}
	list := make([]pair, 0, len(scores))
	for id, a := range scores {
		if a.score > 0 {
			list = append(list, pair{id, a.score})
		}
	}
	sort.SliceStable(list, func(i, j int) bool {
		if list[i].score == list[j].score {
			return list[i].id < list[j].id
		}
		return list[i].score > list[j].score
	})
	out := make([]string, len(list))
	for i, p := range list {
		out[i] = p.id
	}
	return out, nil
}
