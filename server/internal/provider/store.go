package provider

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"yanzhi/server/internal/apperr"
	"yanzhi/server/internal/platform/cryptobox"
	"yanzhi/server/internal/platform/db"
)

type Store struct {
	Pool *pgxpool.Pool
	Key  []byte
	HTTP Client
}

type Saved struct {
	Kind      string `json:"kind"`
	BaseURL   string `json:"base_url"`
	ModelName string `json:"model_name"`
	APIKey    string `json:"api_key"`
	Dimension *int   `json:"dimension"`
}

func (s Store) List(ctx context.Context) ([]Saved, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT kind, base_url, model_name, api_key_last4, dimension
		FROM model_configs ORDER BY kind`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Saved
	for rows.Next() {
		var item Saved
		var last4 string
		var dim *int
		if err := rows.Scan(&item.Kind, &item.BaseURL, &item.ModelName, &last4, &dim); err != nil {
			return nil, err
		}
		item.APIKey = cryptobox.Mask(strings.TrimSpace(last4))
		item.Dimension = dim
		out = append(out, item)
	}
	if out == nil {
		out = []Saved{}
	}
	return out, rows.Err()
}

func (s Store) Load(ctx context.Context, kind string) (Remote, error) {
	var nonce, ct []byte
	var remote Remote
	var dim *int
	err := s.Pool.QueryRow(ctx, `
		SELECT base_url, model_name, api_key_nonce, api_key_ciphertext, dimension
		FROM model_configs WHERE kind = $1`, kind).Scan(&remote.BaseURL, &remote.Model, &nonce, &ct, &dim)
	if errors.Is(err, pgx.ErrNoRows) {
		return Remote{}, apperr.ErrModelMissing
	}
	if err != nil {
		return Remote{}, err
	}
	plain, err := cryptobox.Open(s.Key, nonce, ct)
	if err != nil {
		return Remote{}, apperr.ErrModelMissing
	}
	remote.Kind = kind
	remote.APIKey = string(plain)
	if dim != nil {
		remote.Dimension = *dim
	}
	return remote, nil
}

func (s Store) ActiveGeneration(ctx context.Context) (int64, error) {
	var id int64
	err := s.Pool.QueryRow(ctx, `SELECT id FROM embedding_generations WHERE active LIMIT 1`).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	return id, err
}

type PutInput struct {
	BaseURL        string
	ModelName      string
	APIKey         *string
	Dimension      *int
	ConfirmReindex bool
}

func (s Store) Put(ctx context.Context, kind string, in PutInput) (Saved, error) {
	if !validKind(kind) {
		return Saved{}, apperr.WithDetails("VALIDATION_ERROR", "模型类型不正确", map[string]any{"field": "kind"})
	}
	in.BaseURL = strings.TrimRight(strings.TrimSpace(in.BaseURL), "/")
	in.ModelName = strings.TrimSpace(in.ModelName)
	if in.BaseURL == "" || in.ModelName == "" {
		return Saved{}, apperr.WithDetails("VALIDATION_ERROR", "地址和模型名不能为空", map[string]any{"field": "base_url"})
	}
	if kind == "embedding" {
		if in.Dimension == nil || *in.Dimension < 1 || *in.Dimension > 4096 {
			return Saved{}, apperr.WithDetails("VALIDATION_ERROR", "向量维度需要在 1 到 4096 之间", map[string]any{"field": "dimension"})
		}
	}
	current, err := s.Load(ctx, kind)
	missing := errors.Is(err, apperr.ErrModelMissing)
	if err != nil && !missing {
		return Saved{}, err
	}
	apiKey := ""
	if in.APIKey != nil {
		apiKey = *in.APIKey
	}
	if apiKey == "" {
		if missing {
			return Saved{}, apperr.WithDetails("VALIDATION_ERROR", "请填写 API 密钥", map[string]any{"field": "api_key"})
		}
		apiKey = current.APIKey
	}
	if kind == "embedding" && !missing {
		changed := current.Model != in.ModelName || current.Dimension != *in.Dimension
		if changed {
			n, err := s.vectorCount(ctx)
			if err != nil {
				return Saved{}, err
			}
			if n > 0 && !in.ConfirmReindex {
				return Saved{}, apperr.ErrReindexConfirm
			}
		}
	}
	nonce, ct, err := cryptobox.Seal(s.Key, []byte(apiKey))
	if err != nil {
		return Saved{}, err
	}
	last4 := cryptobox.Last4(apiKey)
	if len([]rune(last4)) < 4 {
		last4 = last4 + "    "
		last4 = last4[:4]
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return Saved{}, err
	}
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, `
		INSERT INTO model_configs (kind, base_url, model_name, api_key_nonce, api_key_ciphertext, api_key_last4, dimension, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, now())
		ON CONFLICT (kind) DO UPDATE SET
			base_url = EXCLUDED.base_url,
			model_name = EXCLUDED.model_name,
			api_key_nonce = EXCLUDED.api_key_nonce,
			api_key_ciphertext = EXCLUDED.api_key_ciphertext,
			api_key_last4 = EXCLUDED.api_key_last4,
			dimension = EXCLUDED.dimension,
			updated_at = now()`,
		kind, in.BaseURL, in.ModelName, nonce, ct, last4, in.Dimension)
	if err != nil {
		return Saved{}, err
	}
	if kind == "embedding" && (missing || current.Model != in.ModelName || current.Dimension != *in.Dimension) {
		if err := s.reindex(ctx, tx, in.ModelName, *in.Dimension); err != nil {
			return Saved{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return Saved{}, err
	}
	dim := in.Dimension
	return Saved{Kind: kind, BaseURL: in.BaseURL, ModelName: in.ModelName, APIKey: cryptobox.Mask(strings.TrimSpace(last4)), Dimension: dim}, nil
}

func (s Store) vectorCount(ctx context.Context) (int64, error) {
	var n int64
	err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM chunks WHERE embedding IS NOT NULL`).Scan(&n)
	return n, err
}

func (s Store) reindex(ctx context.Context, tx pgx.Tx, model string, dim int) error {
	var currentDim int
	err := tx.QueryRow(ctx, `SELECT dimension FROM embedding_generations WHERE active LIMIT 1`).Scan(&currentDim)
	if errors.Is(err, pgx.ErrNoRows) {
		currentDim = 1024
	} else if err != nil {
		return err
	}
	if currentDim != dim {
		stmt := fmt.Sprintf("ALTER TABLE chunks ALTER COLUMN embedding TYPE vector(%d) USING NULL", dim)
		if _, err := tx.Exec(ctx, stmt); err != nil {
			return err
		}
	} else if _, err := tx.Exec(ctx, `UPDATE chunks SET embedding = NULL`); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE embedding_generations SET active = false WHERE active`); err != nil {
		return err
	}
	var gen int64
	if err := tx.QueryRow(ctx, `
		INSERT INTO embedding_generations (model_name, dimension, active) VALUES ($1, $2, true) RETURNING id`,
		model, dim).Scan(&gen); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE kg_relations SET suspended = true WHERE suspended = false`); err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `
		SELECT DISTINCT d.id::text, d.owner_id::text
		FROM documents d
		JOIN chunks c ON c.document_id = d.id`)
	if err != nil {
		return err
	}
	type pair struct{ doc, owner string }
	var docs []pair
	for rows.Next() {
		var p pair
		if err := rows.Scan(&p.doc, &p.owner); err != nil {
			rows.Close()
			return err
		}
		docs = append(docs, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, p := range docs {
		tag, err := tx.Exec(ctx, `
			UPDATE documents
			SET status = 'pending', failure_reason = NULL, graph_status = NULL, graph_failure_reason = NULL,
			    embedding_generation = $2, cancel_requested = false, updated_at = now()
			WHERE id = $1`, db.MustUUID(p.doc), gen)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			continue
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO jobs (id, kind, owner_id, target_id, request_id, priority, attempt, status, created_at)
			VALUES ($1, 'reembed', $2, $3, $4, 10, 1, 'pending', now())`,
			db.MustUUID(db.NewID()), db.MustUUID(p.owner), db.MustUUID(p.doc), db.MustUUID(db.NewID()))
		if err != nil {
			var pe *pgconn.PgError
			if errors.As(err, &pe) && pe.Code == "23505" {
				continue
			}
			return err
		}
	}
	return nil
}

func (s Store) ResolveKey(ctx context.Context, kind, base, model string, key *string, dim *int) (Remote, error) {
	remote := Remote{Kind: kind, BaseURL: strings.TrimRight(strings.TrimSpace(base), "/"), Model: strings.TrimSpace(model)}
	if dim != nil {
		remote.Dimension = *dim
	}
	if key != nil && *key != "" {
		remote.APIKey = *key
		return remote, nil
	}
	saved, err := s.Load(ctx, kind)
	if err != nil {
		return remote, nil
	}
	if remote.BaseURL == "" {
		remote.BaseURL = saved.BaseURL
	}
	if remote.Model == "" {
		remote.Model = saved.Model
	}
	remote.APIKey = saved.APIKey
	return remote, nil
}

func validKind(kind string) bool {
	switch kind {
	case "chat", "embedding", "rerank", "vision":
		return true
	default:
		return false
	}
}

