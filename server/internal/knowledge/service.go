package knowledge

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"yanzhi/server/internal/apperr"
	"yanzhi/server/internal/graph"
	"yanzhi/server/internal/identity"
	"yanzhi/server/internal/ingest"
	"yanzhi/server/internal/jobs"
	"yanzhi/server/internal/pdfcheck"
	"yanzhi/server/internal/platform/clock"
	"yanzhi/server/internal/platform/cryptobox"
	"yanzhi/server/internal/platform/db"
	"yanzhi/server/internal/platform/files"
)

type Service struct {
	Pool  *pgxpool.Pool
	Files files.Store
}

type KB struct {
	ID                 string   `json:"id"`
	Name               string   `json:"name"`
	GraphEnabled       bool     `json:"graph_enabled"`
	AnswerInstruction  *string  `json:"answer_instruction"`
	RetrievalThreshold *float64 `json:"retrieval_threshold"`
	DocumentCount      int      `json:"document_count,omitempty"`
	ReadyCount         int      `json:"ready_count,omitempty"`
}

type Tag struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type Doc struct {
	ID               string   `json:"id"`
	KBID             string   `json:"kb_id"`
	Title            string   `json:"title"`
	Status           string   `json:"status"`
	GraphStatus      any      `json:"graph_status"`
	Year             *int     `json:"year"`
	Authors          []string `json:"authors"`
	Tags             []Tag    `json:"tags"`
	FailureReason    *string  `json:"failure_reason"`
	SkippedPages     []int    `json:"skipped_pages"`
	RelativePath     *string  `json:"relative_path"`
	Venue            *string  `json:"venue,omitempty"`
	Volume           *string  `json:"volume,omitempty"`
	Issue            *string  `json:"issue,omitempty"`
	Pages            *string  `json:"pages,omitempty"`
	DOI              *string  `json:"doi,omitempty"`
	PageCount        *int     `json:"page_count,omitempty"`
	HasPhysicalPages *bool    `json:"has_physical_pages,omitempty"`
	ManualRetryCount *int     `json:"manual_retry_count,omitempty"`
	SizeBytes        *int64   `json:"size_bytes,omitempty"`
	CreatedAt        string   `json:"created_at,omitempty"`
}

type UploadMeta struct {
	FileName       string
	Title          string
	Authors        []string
	Year           *int
	Venue          string
	RelativePath   string
	IdempotencyKey string
	RequestID      string
}

type UploadResult struct {
	Status int
	Doc    Doc
}

func (s Service) ListKBs(ctx context.Context, owner string) ([]KB, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT k.id::text, k.name, k.graph_enabled, k.answer_instruction, k.retrieval_threshold::float8,
		       (SELECT count(*) FROM documents d WHERE d.kb_id = k.id),
		       (SELECT count(*) FROM documents d WHERE d.kb_id = k.id AND d.status = 'ready')
		FROM knowledge_bases k
		WHERE k.owner_id = $1
		ORDER BY k.created_at DESC`, db.MustUUID(owner))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []KB{}
	for rows.Next() {
		var kb KB
		if err := rows.Scan(&kb.ID, &kb.Name, &kb.GraphEnabled, &kb.AnswerInstruction, &kb.RetrievalThreshold, &kb.DocumentCount, &kb.ReadyCount); err != nil {
			return nil, err
		}
		out = append(out, kb)
	}
	return out, rows.Err()
}

func (s Service) CreateKB(ctx context.Context, owner, name string) (KB, error) {
	name = strings.TrimSpace(name)
	if !NameOK(name) {
		return KB{}, apperr.WithDetails("VALIDATION_ERROR", "名称需要 1 到 50 个字符", map[string]any{"field": "name"})
	}
	id := db.NewID()
	_, err := s.Pool.Exec(ctx, `
		INSERT INTO knowledge_bases (id, owner_id, name, graph_enabled, created_at, updated_at)
		VALUES ($1, $2, $3, true, now(), now())`, db.MustUUID(id), db.MustUUID(owner), name)
	if err != nil {
		return KB{}, err
	}
	return KB{ID: id, Name: name, GraphEnabled: true}, nil
}

func (s Service) GetKB(ctx context.Context, owner, id string) (KB, error) {
	var kb KB
	err := s.Pool.QueryRow(ctx, `
		SELECT id::text, name, graph_enabled, answer_instruction, retrieval_threshold::float8,
		       (SELECT count(*) FROM documents d WHERE d.kb_id = knowledge_bases.id),
		       (SELECT count(*) FROM documents d WHERE d.kb_id = knowledge_bases.id AND d.status = 'ready')
		FROM knowledge_bases WHERE id = $1 AND owner_id = $2`, db.MustUUID(id), db.MustUUID(owner)).Scan(
		&kb.ID, &kb.Name, &kb.GraphEnabled, &kb.AnswerInstruction, &kb.RetrievalThreshold, &kb.DocumentCount, &kb.ReadyCount)
	if errors.Is(err, pgx.ErrNoRows) {
		return KB{}, apperr.ErrNotFound
	}
	return kb, err
}

func (s Service) PatchKB(ctx context.Context, owner, id string, raw map[string]json.RawMessage) (KB, error) {
	if _, err := s.GetKB(ctx, owner, id); err != nil {
		return KB{}, err
	}
	if v, ok := raw["answer_instruction"]; ok {
		if string(v) == "null" {
			if _, err := s.Pool.Exec(ctx, `UPDATE knowledge_bases SET answer_instruction = NULL, updated_at = now() WHERE id = $1`, db.MustUUID(id)); err != nil {
				return KB{}, err
			}
		} else {
			var text string
			if err := json.Unmarshal(v, &text); err != nil {
				return KB{}, apperr.WithDetails("VALIDATION_ERROR", "回答说明格式不正确", map[string]any{"field": "answer_instruction"})
			}
			if len([]rune(text)) > 2000 {
				return KB{}, apperr.WithDetails("VALIDATION_ERROR", "回答说明不能超过 2000 个字符", map[string]any{"field": "answer_instruction"})
			}
			if _, err := s.Pool.Exec(ctx, `UPDATE knowledge_bases SET answer_instruction = $2, updated_at = now() WHERE id = $1`, db.MustUUID(id), text); err != nil {
				return KB{}, err
			}
		}
	}
	if v, ok := raw["retrieval_threshold"]; ok {
		if string(v) == "null" {
			if _, err := s.Pool.Exec(ctx, `UPDATE knowledge_bases SET retrieval_threshold = NULL, updated_at = now() WHERE id = $1`, db.MustUUID(id)); err != nil {
				return KB{}, err
			}
		} else {
			var n float64
			if err := json.Unmarshal(v, &n); err != nil || n < 0 || n > 1 {
				return KB{}, apperr.WithDetails("VALIDATION_ERROR", "检索阈值需要在 0 到 1 之间", map[string]any{"field": "retrieval_threshold"})
			}
			if _, err := s.Pool.Exec(ctx, `UPDATE knowledge_bases SET retrieval_threshold = $2, updated_at = now() WHERE id = $1`, db.MustUUID(id), n); err != nil {
				return KB{}, err
			}
		}
	}
	if v, ok := raw["graph_enabled"]; ok {
		enabled := true
		if string(v) != "null" {
			if err := json.Unmarshal(v, &enabled); err != nil {
				return KB{}, apperr.WithDetails("VALIDATION_ERROR", "图谱开关格式不正确", map[string]any{"field": "graph_enabled"})
			}
		}
		if _, err := s.Pool.Exec(ctx, `UPDATE knowledge_bases SET graph_enabled = $2, updated_at = now() WHERE id = $1`, db.MustUUID(id), enabled); err != nil {
			return KB{}, err
		}
	}
	return s.GetKB(ctx, owner, id)
}

func (s Service) Upload(ctx context.Context, owner, kbID string, meta UploadMeta, data []byte) (UploadResult, error) {
	var ownerMatch string
	err := s.Pool.QueryRow(ctx, `SELECT owner_id::text FROM knowledge_bases WHERE id = $1`, db.MustUUID(kbID)).Scan(&ownerMatch)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && ownerMatch != owner) {
		return UploadResult{}, apperr.ErrNotFound
	}
	if err != nil {
		return UploadResult{}, err
	}
	title := strings.TrimSpace(meta.Title)
	if title == "" || len([]rune(title)) > 500 {
		return UploadResult{}, apperr.WithDetails("VALIDATION_ERROR", "题名不能为空，且不超过 500 个字符", map[string]any{"field": "title"})
	}
	if meta.Authors == nil {
		meta.Authors = []string{}
	}
	var storage int64
	if err := s.Pool.QueryRow(ctx, `SELECT storage_bytes FROM users WHERE id = $1`, db.MustUUID(owner)).Scan(&storage); err != nil {
		return UploadResult{}, err
	}
	info := pdfcheck.Inspect(data)
	ext := strings.ToLower(meta.FileName)
	isPDF := info.IsPDF && strings.HasSuffix(ext, ".pdf")
	sum := cryptobox.SHA256Hex(data)
	// 幂等键先于「同哈希已存在」判定：网络重试应复用第一次的 201，而不是变成 409。
	hash := cryptobox.SHA256Hex(append(append([]byte(sum), []byte(kbID+title+meta.Venue+meta.RelativePath)...), mustJSON(meta.Authors)...))
	if meta.IdempotencyKey != "" {
		if replay, ok, err := s.replay(ctx, owner, meta.IdempotencyKey, hash); err != nil || ok {
			return replay, err
		}
	}
	var existing *Existing
	var ex Existing
	var key string
	err = s.Pool.QueryRow(ctx, `
		SELECT id::text, kb_id::text, status, COALESCE(storage_key, '')
		FROM documents WHERE owner_id = $1 AND sha256 = $2`, db.MustUUID(owner), sum).Scan(&ex.ID, &ex.KBID, &ex.Status, &key)
	if err == nil {
		existing = &ex
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return UploadResult{}, err
	}
	outcome := DecideUpload(UploadInput{
		KBOwned: true, Size: int64(len(data)), IsPDF: isPDF, Encrypted: info.Encrypted,
		PageCount: info.PageCount, StorageBytes: storage, Existing: existing,
	})
	if outcome.Err != nil {
		return UploadResult{}, outcome.Err
	}
	req := meta.RequestID
	if _, ok := db.UUID(req); !ok {
		req = db.NewID()
	}
	if outcome.Reuse {
		if key == "" {
			key = owner + "/" + existing.ID
		}
		if err := s.Files.Put(key, data); err != nil {
			return UploadResult{}, err
		}
		authors := mustJSON(meta.Authors)
		tx, err := s.Pool.Begin(ctx)
		if err != nil {
			return UploadResult{}, err
		}
		defer tx.Rollback(ctx)
		if err := ingest.DeleteDerived(ctx, tx, owner, existing.ID); err != nil {
			return UploadResult{}, err
		}
		tag, err := tx.Exec(ctx, `
			UPDATE documents SET status = 'pending', failure_reason = NULL, kb_id = $2, title = $3,
				authors = $4, year = $5, venue = NULLIF($6, ''), relative_path = NULLIF($7, ''),
				storage_key = $8, size_bytes = $9, cancel_requested = false, graph_status = NULL,
				graph_failure_reason = NULL, page_count = NULL, skipped_pages = '{}', updated_at = now()
			WHERE id = $1 AND owner_id = $10 AND status = 'failed'`,
			db.MustUUID(existing.ID), db.MustUUID(kbID), title, authors, meta.Year, meta.Venue, cleanPath(meta.RelativePath), key, len(data), db.MustUUID(owner))
		if err != nil {
			return UploadResult{}, err
		}
		if tag.RowsAffected() == 0 {
			return UploadResult{}, apperr.ErrDuplicateDoc
		}
		if err := tx.Commit(ctx); err != nil {
			return UploadResult{}, err
		}
		if err := jobs.Enqueue(ctx, s.Pool, "ingest", owner, existing.ID, req, 10, 1); err != nil {
			return UploadResult{}, err
		}
		if err := s.remember(ctx, owner, meta.IdempotencyKey, hash, existing.ID, 200); err != nil {
			return UploadResult{}, err
		}
		doc, err := s.GetDoc(ctx, owner, existing.ID)
		return UploadResult{Status: 200, Doc: doc}, err
	}
	id := db.NewID()
	storageKey := owner + "/" + id
	if err := s.Files.Put(storageKey, data); err != nil {
		return UploadResult{}, err
	}
	var gen int64
	_ = s.Pool.QueryRow(ctx, `SELECT id FROM embedding_generations WHERE active LIMIT 1`).Scan(&gen)
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		_ = s.Files.Delete(storageKey)
		return UploadResult{}, err
	}
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, `
		INSERT INTO documents (
			id, owner_id, kb_id, source_type, title, authors, year, venue, relative_path,
			status, sha256, size_bytes, storage_key, embedding_generation, created_at, updated_at
		) VALUES (
			$1,$2,$3,'pdf',$4,$5,$6,NULLIF($7,''),NULLIF($8,''),
			'pending',$9,$10,$11,$12, now(), now()
		)`,
		db.MustUUID(id), db.MustUUID(owner), db.MustUUID(kbID), title, mustJSON(meta.Authors), meta.Year, meta.Venue,
		cleanPath(meta.RelativePath), sum, len(data), storageKey, gen)
	if err != nil {
		_ = s.Files.Delete(storageKey)
		var pe *pgconn.PgError
		if errors.As(err, &pe) && pe.Code == "23505" {
			return UploadResult{}, apperr.ErrDuplicateDoc
		}
		return UploadResult{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE users SET storage_bytes = storage_bytes + $2 WHERE id = $1`, db.MustUUID(owner), len(data)); err != nil {
		_ = s.Files.Delete(storageKey)
		return UploadResult{}, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO jobs (id, kind, owner_id, target_id, request_id, priority, attempt, status, created_at)
		VALUES ($1, 'ingest', $2, $3, $4, 10, 1, 'pending', now())`,
		db.MustUUID(db.NewID()), db.MustUUID(owner), db.MustUUID(id), db.MustUUID(req)); err != nil {
		_ = s.Files.Delete(storageKey)
		return UploadResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		_ = s.Files.Delete(storageKey)
		return UploadResult{}, err
	}
	if err := s.remember(ctx, owner, meta.IdempotencyKey, hash, id, 201); err != nil {
		return UploadResult{}, err
	}
	doc, err := s.GetDoc(ctx, owner, id)
	return UploadResult{Status: 201, Doc: doc}, err
}

func (s Service) replay(ctx context.Context, owner, key, hash string) (UploadResult, bool, error) {
	_, _ = s.Pool.Exec(ctx, `DELETE FROM idempotency_keys WHERE user_id = $1 AND key = $2 AND created_at < now() - interval '24 hours'`, db.MustUUID(owner), key)
	var storedHash string
	var docID *string
	var status int
	err := s.Pool.QueryRow(ctx, `
		SELECT request_hash, document_id::text, status_code FROM idempotency_keys
		WHERE user_id = $1 AND key = $2`, db.MustUUID(owner), key).Scan(&storedHash, &docID, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return UploadResult{}, false, nil
	}
	if err != nil {
		return UploadResult{}, false, err
	}
	if storedHash != hash {
		return UploadResult{}, false, apperr.ErrIdempotency
	}
	if docID == nil {
		return UploadResult{}, false, apperr.ErrNotFound
	}
	doc, err := s.GetDoc(ctx, owner, *docID)
	if errors.Is(err, apperr.ErrNotFound) {
		_, _ = s.Pool.Exec(ctx, `DELETE FROM idempotency_keys WHERE user_id = $1 AND key = $2`, db.MustUUID(owner), key)
		return UploadResult{}, false, nil
	}
	if err != nil {
		return UploadResult{}, false, err
	}
	return UploadResult{Status: status, Doc: doc}, true, nil
}

func (s Service) remember(ctx context.Context, owner, key, hash, docID string, status int) error {
	if key == "" {
		return nil
	}
	_, err := s.Pool.Exec(ctx, `
		INSERT INTO idempotency_keys (id, user_id, key, request_hash, document_id, status_code, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, now())`,
		db.MustUUID(db.NewID()), db.MustUUID(owner), key, hash, db.MustUUID(docID), status)
	var pe *pgconn.PgError
	if errors.As(err, &pe) && pe.Code == "23505" {
		replay, ok, rerr := s.replay(ctx, owner, key, hash)
		if rerr != nil {
			return rerr
		}
		if ok && replay.Doc.ID == docID {
			return nil
		}
		return apperr.ErrIdempotency
	}
	return err
}

func (s Service) ListDocs(ctx context.Context, owner, kbID, q string, tagIDs []string, page, size int) ([]Doc, int, error) {
	if page < 1 {
		page = 1
	}
	if size < 1 || size > 100 {
		size = 20
	}
	pattern := ""
	if strings.TrimSpace(q) != "" {
		pattern = "%" + likeEscape(strings.ToLower(strings.TrimSpace(q))) + "%"
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT d.id::text, d.kb_id::text, d.title, d.status, d.graph_status, d.year, d.authors,
		       d.failure_reason, d.skipped_pages, d.relative_path, d.created_at,
		       COALESCE((SELECT json_agg(json_build_object('id', t.id::text, 'name', t.name))
		                 FROM document_tags dt JOIN tags t ON t.id = dt.tag_id WHERE dt.document_id = d.id), '[]')
		FROM documents d
		WHERE d.owner_id = $1
		  AND ($2::uuid IS NULL OR d.kb_id = $2)
		  AND ($3 = '' OR lower(d.title) LIKE $3 ESCAPE '\')
		  AND (cardinality($4::uuid[]) = 0 OR EXISTS (
		      SELECT 1 FROM document_tags dt WHERE dt.document_id = d.id AND dt.tag_id = ANY($4::uuid[])))
		ORDER BY d.created_at DESC
		LIMIT $5 OFFSET $6`, db.MustUUID(owner), nullID(kbID), pattern, uuidSlice(tagIDs), size, (page-1)*size)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []Doc{}
	for rows.Next() {
		doc, err := scanDoc(rows, false)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, doc)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	var total int
	err = s.Pool.QueryRow(ctx, `
		SELECT count(*) FROM documents d
		WHERE d.owner_id = $1
		  AND ($2::uuid IS NULL OR d.kb_id = $2)
		  AND ($3 = '' OR lower(d.title) LIKE $3 ESCAPE '\')
		  AND (cardinality($4::uuid[]) = 0 OR EXISTS (
		      SELECT 1 FROM document_tags dt WHERE dt.document_id = d.id AND dt.tag_id = ANY($4::uuid[])))`,
		db.MustUUID(owner), nullID(kbID), pattern, uuidSlice(tagIDs)).Scan(&total)
	return out, total, err
}

func (s Service) GetDoc(ctx context.Context, owner, id string) (Doc, error) {
	row := s.Pool.QueryRow(ctx, `
		SELECT d.id::text, d.kb_id::text, d.title, d.status, d.graph_status, d.year, d.authors,
		       d.failure_reason, d.skipped_pages, d.relative_path, d.created_at,
		       COALESCE((SELECT json_agg(json_build_object('id', t.id::text, 'name', t.name))
		                 FROM document_tags dt JOIN tags t ON t.id = dt.tag_id WHERE dt.document_id = d.id), '[]'),
		       d.venue, d.volume, d.issue, d.pages, d.doi, d.page_count, d.has_physical_pages,
		       d.manual_retry_count, d.size_bytes
		FROM documents d WHERE d.id = $1 AND d.owner_id = $2`, db.MustUUID(id), db.MustUUID(owner))
	return scanDoc(row, true)
}

func (s Service) PatchDoc(ctx context.Context, owner, id string, raw map[string]json.RawMessage) (Doc, error) {
	if _, err := s.GetDoc(ctx, owner, id); err != nil {
		return Doc{}, err
	}
	if v, ok := raw["title"]; ok {
		var title string
		if json.Unmarshal(v, &title) != nil || strings.TrimSpace(title) == "" || len([]rune(title)) > 500 {
			return Doc{}, apperr.WithDetails("VALIDATION_ERROR", "题名不能为空，且不超过 500 个字符", map[string]any{"field": "title"})
		}
		if _, err := s.Pool.Exec(ctx, `UPDATE documents SET title = $2, updated_at = now() WHERE id = $1`, db.MustUUID(id), strings.TrimSpace(title)); err != nil {
			return Doc{}, err
		}
	}
	if v, ok := raw["authors"]; ok {
		var authors []string
		if json.Unmarshal(v, &authors) != nil {
			return Doc{}, apperr.WithDetails("VALIDATION_ERROR", "作者需要是字符串数组", map[string]any{"field": "authors"})
		}
		if _, err := s.Pool.Exec(ctx, `UPDATE documents SET authors = $2, updated_at = now() WHERE id = $1`, db.MustUUID(id), mustJSON(authors)); err != nil {
			return Doc{}, err
		}
	}
	for _, field := range []string{"venue", "volume", "issue", "pages", "doi"} {
		v, ok := raw[field]
		if !ok {
			continue
		}
		if string(v) == "null" {
			if _, err := s.Pool.Exec(ctx, `UPDATE documents SET `+field+` = NULL, updated_at = now() WHERE id = $1`, db.MustUUID(id)); err != nil {
				return Doc{}, err
			}
			continue
		}
		var text string
		if json.Unmarshal(v, &text) != nil {
			return Doc{}, apperr.WithDetails("VALIDATION_ERROR", "字段格式不正确", map[string]any{"field": field})
		}
		if _, err := s.Pool.Exec(ctx, `UPDATE documents SET `+field+` = $2, updated_at = now() WHERE id = $1`, db.MustUUID(id), text); err != nil {
			return Doc{}, err
		}
	}
	if v, ok := raw["year"]; ok {
		if string(v) == "null" {
			_, err := s.Pool.Exec(ctx, `UPDATE documents SET year = NULL, updated_at = now() WHERE id = $1`, db.MustUUID(id))
			if err != nil {
				return Doc{}, err
			}
		} else {
			var year int
			if json.Unmarshal(v, &year) != nil || year < 0 || year > 3000 {
				return Doc{}, apperr.WithDetails("VALIDATION_ERROR", "年份不正确", map[string]any{"field": "year"})
			}
			if _, err := s.Pool.Exec(ctx, `UPDATE documents SET year = $2, updated_at = now() WHERE id = $1`, db.MustUUID(id), year); err != nil {
				return Doc{}, err
			}
		}
	}
	return s.GetDoc(ctx, owner, id)
}

func (s Service) Delete(ctx context.Context, owner, id string) error {
	tag, err := s.Pool.Exec(ctx, `UPDATE documents SET cancel_requested = true WHERE id = $1 AND owner_id = $2`, db.MustUUID(id), db.MustUUID(owner))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return apperr.ErrNotFound
	}
	_, _ = s.Pool.Exec(ctx, `UPDATE jobs SET status = 'cancelled', finished_at = now() WHERE target_id = $1 AND status IN ('pending', 'running')`, db.MustUUID(id))
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var size int64
	var storageKey, title string
	err = tx.QueryRow(ctx, `SELECT size_bytes, COALESCE(storage_key, ''), title FROM documents WHERE id = $1 AND owner_id = $2 FOR UPDATE`, db.MustUUID(id), db.MustUUID(owner)).Scan(&size, &storageKey, &title)
	if errors.Is(err, pgx.ErrNoRows) {
		return apperr.ErrNotFound
	}
	if err != nil {
		return err
	}
	_, _ = tx.Exec(ctx, `
		INSERT INTO kg_unresolved_refs (id, document_id, title, created_at)
		SELECT gen_random_uuid(), ev.document_id, $2, now()
		FROM kg_evidence ev
		JOIN kg_relations r ON r.id = ev.relation_id
		JOIN kg_entities e ON e.id = r.target_id
		WHERE r.type = 'cites' AND e.merge_key = $3 AND ev.document_id <> $1`,
		db.MustUUID(id), title, graph.PaperKey(id))
	if err := ingest.DeleteDerived(ctx, tx, owner, id); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE qa_citations SET chunk_id = NULL, label = '来源已删除' WHERE document_id = $1`, db.MustUUID(id)); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE users SET storage_bytes = GREATEST(storage_bytes - $2, 0) WHERE id = $1`, db.MustUUID(owner), size); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM documents WHERE id = $1`, db.MustUUID(id)); err != nil {
		return err
	}
	if err := graph.Cleanup(ctx, tx, owner); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	if storageKey != "" {
		_ = s.Files.Delete(storageKey)
	}
	return nil
}

func (s Service) File(ctx context.Context, owner, id string) (string, []byte, error) {
	var title, key string
	err := s.Pool.QueryRow(ctx, `SELECT title, COALESCE(storage_key, '') FROM documents WHERE id = $1 AND owner_id = $2`, db.MustUUID(id), db.MustUUID(owner)).Scan(&title, &key)
	if errors.Is(err, pgx.ErrNoRows) || key == "" {
		return "", nil, apperr.ErrNotFound
	}
	if err != nil {
		return "", nil, err
	}
	data, err := s.Files.Get(key)
	if err != nil {
		return "", nil, apperr.ErrNotFound
	}
	return title, data, nil
}

func (s Service) Retry(ctx context.Context, owner, id, requestID string) error {
	var status string
	var n int
	err := s.Pool.QueryRow(ctx, `SELECT status, manual_retry_count FROM documents WHERE id = $1 AND owner_id = $2`, db.MustUUID(id), db.MustUUID(owner)).Scan(&status, &n)
	if errors.Is(err, pgx.ErrNoRows) {
		return apperr.ErrNotFound
	}
	if err != nil {
		return err
	}
	if status != "failed" {
		return apperr.ErrNotReady
	}
	if n >= 3 {
		return apperr.ErrRetryExhausted
	}
	tag, err := s.Pool.Exec(ctx, `
		UPDATE documents SET status = 'pending', failure_reason = NULL, cancel_requested = false,
			manual_retry_count = manual_retry_count + 1, updated_at = now()
		WHERE id = $1 AND owner_id = $2 AND status = 'failed' AND manual_retry_count < 3`, db.MustUUID(id), db.MustUUID(owner))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return apperr.ErrRetryExhausted
	}
	if _, ok := db.UUID(requestID); !ok {
		requestID = db.NewID()
	}
	return jobs.Enqueue(ctx, s.Pool, "ingest", owner, id, requestID, 10, n+1)
}

func (s Service) RetryGraph(ctx context.Context, owner, id, requestID string) error {
	var graphStatus *string
	var n int
	var docStatus string
	err := s.Pool.QueryRow(ctx, `
		SELECT status, graph_status, graph_manual_retry_count FROM documents WHERE id = $1 AND owner_id = $2`,
		db.MustUUID(id), db.MustUUID(owner)).Scan(&docStatus, &graphStatus, &n)
	if errors.Is(err, pgx.ErrNoRows) {
		return apperr.ErrNotFound
	}
	if err != nil {
		return err
	}
	if docStatus != "ready" {
		return apperr.ErrNotReady
	}
	if graphStatus == nil || *graphStatus != "failed" {
		return apperr.ErrNotReady
	}
	if n >= 3 {
		return apperr.ErrRetryExhausted
	}
	tag, err := s.Pool.Exec(ctx, `
		UPDATE documents SET graph_status = 'pending', graph_failure_reason = NULL,
			graph_manual_retry_count = graph_manual_retry_count + 1, updated_at = now()
		WHERE id = $1 AND graph_status = 'failed' AND graph_manual_retry_count < 3`, db.MustUUID(id))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return apperr.ErrRetryExhausted
	}
	if _, ok := db.UUID(requestID); !ok {
		requestID = db.NewID()
	}
	return jobs.Enqueue(ctx, s.Pool, "graph_extract", owner, id, requestID, 80, n+1)
}

func (s Service) Ingest(ctx context.Context, owner, id string) (map[string]any, error) {
	doc, err := s.GetDoc(ctx, owner, id)
	if err != nil {
		return nil, err
	}
	var attempt int
	var beat *string
	var jobID string
	err = s.Pool.QueryRow(ctx, `
		SELECT id::text, attempt, to_char(heartbeat_at AT TIME ZONE 'Asia/Shanghai', 'YYYY-MM-DD"T"HH24:MI:SS+08:00')
		FROM jobs WHERE target_id = $1 AND kind IN ('ingest', 'reembed')
		ORDER BY created_at DESC LIMIT 1`, db.MustUUID(id)).Scan(&jobID, &attempt, &beat)
	stages := []map[string]string{}
	if err == nil {
		rows, qerr := s.Pool.Query(ctx, `SELECT name, status FROM job_stages WHERE job_id = $1`, db.MustUUID(jobID))
		if qerr != nil {
			return nil, qerr
		}
		defer rows.Close()
		got := map[string]string{}
		for rows.Next() {
			var name, st string
			if err := rows.Scan(&name, &st); err != nil {
				return nil, err
			}
			got[name] = st
		}
		for _, name := range []string{"fetch", "parse", "recognize", "chunk", "embed"} {
			if st, ok := got[name]; ok {
				stages = append(stages, map[string]string{"name": name, "status": st})
			}
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	retries := 0
	if doc.ManualRetryCount != nil {
		retries = *doc.ManualRetryCount
	}
	return map[string]any{
		"document_status": doc.Status, "attempt": attempt, "manual_retry_count": retries,
		"heartbeat_at": beat, "stages": stages,
	}, nil
}

func (s Service) ListTags(ctx context.Context, owner string) ([]Tag, error) {
	rows, err := s.Pool.Query(ctx, `SELECT id::text, name FROM tags WHERE owner_id = $1 ORDER BY created_at`, db.MustUUID(owner))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Tag{}
	for rows.Next() {
		var t Tag
		if err := rows.Scan(&t.ID, &t.Name); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s Service) CreateTag(ctx context.Context, owner, name string) (Tag, error) {
	name = strings.TrimSpace(name)
	if !NameOK(name) {
		return Tag{}, apperr.WithDetails("VALIDATION_ERROR", "名称需要 1 到 50 个字符", map[string]any{"field": "name"})
	}
	id := db.NewID()
	_, err := s.Pool.Exec(ctx, `INSERT INTO tags (id, owner_id, name, created_at) VALUES ($1, $2, $3, now())`, db.MustUUID(id), db.MustUUID(owner), name)
	var pe *pgconn.PgError
	if errors.As(err, &pe) && pe.Code == "23505" {
		return Tag{}, apperr.ErrTagExists
	}
	if err != nil {
		return Tag{}, err
	}
	return Tag{ID: id, Name: name}, nil
}

func (s Service) PatchTag(ctx context.Context, owner, id, name string) (Tag, error) {
	name = strings.TrimSpace(name)
	if !NameOK(name) {
		return Tag{}, apperr.WithDetails("VALIDATION_ERROR", "名称需要 1 到 50 个字符", map[string]any{"field": "name"})
	}
	tag, err := s.Pool.Exec(ctx, `UPDATE tags SET name = $3 WHERE id = $1 AND owner_id = $2`, db.MustUUID(id), db.MustUUID(owner), name)
	var pe *pgconn.PgError
	if errors.As(err, &pe) && pe.Code == "23505" {
		return Tag{}, apperr.ErrTagExists
	}
	if err != nil {
		return Tag{}, err
	}
	if tag.RowsAffected() == 0 {
		return Tag{}, apperr.ErrNotFound
	}
	return Tag{ID: id, Name: name}, nil
}

func (s Service) DeleteTag(ctx context.Context, owner, id string) error {
	tag, err := s.Pool.Exec(ctx, `DELETE FROM tags WHERE id = $1 AND owner_id = $2`, db.MustUUID(id), db.MustUUID(owner))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return apperr.ErrNotFound
	}
	return nil
}

func (s Service) PutTags(ctx context.Context, owner, docID string, tagIDs []string) (Doc, error) {
	if _, err := s.GetDoc(ctx, owner, docID); err != nil {
		return Doc{}, err
	}
	ids, ok := db.UUIDs(tagIDs)
	if !ok {
		return Doc{}, apperr.ErrNotFound
	}
	if len(ids) > 0 {
		var n int
		if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM tags WHERE owner_id = $1 AND id = ANY($2::uuid[])`, db.MustUUID(owner), ids).Scan(&n); err != nil {
			return Doc{}, err
		}
		if n != len(ids) {
			return Doc{}, apperr.ErrNotFound
		}
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return Doc{}, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `DELETE FROM document_tags WHERE document_id = $1`, db.MustUUID(docID)); err != nil {
		return Doc{}, err
	}
	for _, id := range ids {
		if _, err := tx.Exec(ctx, `INSERT INTO document_tags (document_id, tag_id) VALUES ($1, $2)`, db.MustUUID(docID), id); err != nil {
			return Doc{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return Doc{}, err
	}
	return s.GetDoc(ctx, owner, docID)
}

func (s Service) Chunks(ctx context.Context, owner, docID string, page, size int) ([]map[string]any, int, error) {
	if _, err := s.GetDoc(ctx, owner, docID); err != nil {
		return nil, 0, err
	}
	if page < 1 {
		page = 1
	}
	if size < 1 || size > 100 {
		size = 20
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT id::text, text, page_start, kind, version_no, paragraph_index
		FROM chunks WHERE document_id = $1 ORDER BY paragraph_index LIMIT $2 OFFSET $3`,
		db.MustUUID(docID), size, (page-1)*size)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, text, kind string
		var pageStart, version, para int
		if err := rows.Scan(&id, &text, &pageStart, &kind, &version, &para); err != nil {
			return nil, 0, err
		}
		out = append(out, map[string]any{"id": id, "text": text, "page_start": pageStart, "kind": kind, "version_no": version, "paragraph_index": para})
	}
	var total int
	err = s.Pool.QueryRow(ctx, `SELECT count(*) FROM chunks WHERE document_id = $1`, db.MustUUID(docID)).Scan(&total)
	return out, total, err
}

func (s Service) Chunk(ctx context.Context, owner, docID, chunkID string) (map[string]any, error) {
	var text, parent string
	var page int
	err := s.Pool.QueryRow(ctx, `
		SELECT c.text, c.page_start, p.text
		FROM chunks c
		JOIN parent_chunks p ON p.id = c.parent_id
		JOIN documents d ON d.id = c.document_id
		WHERE c.id = $1 AND d.id = $2 AND d.owner_id = $3`,
		db.MustUUID(chunkID), db.MustUUID(docID), db.MustUUID(owner)).Scan(&text, &page, &parent)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, apperr.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return map[string]any{"text": text, "page_start": page, "parent_text": parent}, nil
}

type scanner interface {
	Scan(dest ...any) error
}

func scanDoc(row scanner, detail bool) (Doc, error) {
	var doc Doc
	var status string
	var authorsJSON []byte
	var graph *string
	var skipped []int32
	var createdAt time.Time
	var tagsJSON []byte
	var err error
	if detail {
		var venue, volume, issue, pages, doi *string
		var pageCount *int
		var physical bool
		var retries int
		var size int64
		err = row.Scan(&doc.ID, &doc.KBID, &doc.Title, &status, &graph, &doc.Year, &authorsJSON,
			&doc.FailureReason, &skipped, &doc.RelativePath, &createdAt, &tagsJSON,
			&venue, &volume, &issue, &pages, &doi, &pageCount, &physical, &retries, &size)
		doc.Venue, doc.Volume, doc.Issue, doc.Pages, doc.DOI = venue, volume, issue, pages, doi
		doc.PageCount = pageCount
		doc.HasPhysicalPages = &physical
		doc.ManualRetryCount = &retries
		doc.SizeBytes = &size
	} else {
		err = row.Scan(&doc.ID, &doc.KBID, &doc.Title, &status, &graph, &doc.Year, &authorsJSON,
			&doc.FailureReason, &skipped, &doc.RelativePath, &createdAt, &tagsJSON)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return Doc{}, apperr.ErrNotFound
	}
	if err != nil {
		return Doc{}, err
	}
	doc.Status = identity.DocStatusLabel(status)
	code := ""
	if graph != nil {
		code = *graph
	}
	doc.GraphStatus = identity.GraphStatusLabel(code)
	if json.Unmarshal(authorsJSON, &doc.Authors) != nil || doc.Authors == nil {
		doc.Authors = []string{}
	}
	if json.Unmarshal(tagsJSON, &doc.Tags) != nil || doc.Tags == nil {
		doc.Tags = []Tag{}
	}
	doc.SkippedPages = make([]int, 0, len(skipped))
	for _, n := range skipped {
		doc.SkippedPages = append(doc.SkippedPages, int(n))
	}
	doc.CreatedAt = clock.Format(createdAt)
	return doc, nil
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

func cleanPath(p string) string {
	p = strings.TrimSpace(p)
	if strings.Contains(p, "..") || len(p) > 500 {
		return ""
	}
	return p
}

func likeEscape(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `%`, `\%`)
	s = strings.ReplaceAll(s, `_`, `\_`)
	return s
}

func nullID(s string) any {
	if s == "" {
		return nil
	}
	id, ok := db.UUID(s)
	if !ok {
		return nil
	}
	return id
}

func uuidSlice(ids []string) []pgtype.UUID {
	out, ok := db.UUIDs(ids)
	if !ok || out == nil {
		return []pgtype.UUID{}
	}
	return out
}
