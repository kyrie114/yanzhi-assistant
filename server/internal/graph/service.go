package graph

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"yanzhi/server/internal/apperr"
	"yanzhi/server/internal/jobs"
	"yanzhi/server/internal/platform/db"
	"yanzhi/server/internal/prompts"
	"yanzhi/server/internal/provider"
)

type Service struct {
	Pool    *pgxpool.Pool
	AI      provider.Client
	Models  provider.Store
	Timeout time.Duration
}

type Node struct {
	ID         string  `json:"id"`
	Name       string  `json:"name"`
	Type       string  `json:"type"`
	DocumentID *string `json:"document_id"`
}

type Edge struct {
	ID        string `json:"id"`
	Type      string `json:"type"`
	Source    string `json:"source"`
	Target    string `json:"target"`
	Suspended bool   `json:"suspended"`
}

type View struct {
	Nodes     []Node `json:"nodes"`
	Edges     []Edge `json:"edges"`
	Truncated bool   `json:"truncated"`
}

func (s Service) Extract(ctx context.Context, job jobs.Job) error {
	_ = jobs.Stage(ctx, s.Pool, job.ID, "extract", "running")
	remote, err := s.Models.Load(ctx, "chat")
	if err != nil {
		_ = jobs.Stage(ctx, s.Pool, job.ID, "extract", "failed")
		return s.fail(ctx, job.TargetID, apperr.ErrModelMissing.Message)
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT id::text, text, page_start FROM chunks
		WHERE document_id = $1 ORDER BY paragraph_index`, db.MustUUID(job.TargetID))
	if err != nil {
		return s.fail(ctx, job.TargetID, "抽取结果无法解析")
	}
	var chunks []ChunkView
	for rows.Next() {
		var c ChunkView
		if err := rows.Scan(&c.ID, &c.Text, &c.Page); err != nil {
			rows.Close()
			return err
		}
		chunks = append(chunks, c)
		c.Alias = ""
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	var allEnt []AcceptedEntity
	var allRel []AcceptedRelation
	for start := 0; start < len(chunks); start += 8 {
		end := start + 8
		if end > len(chunks) {
			end = len(chunks)
		}
		batch := append([]ChunkView(nil), chunks[start:end]...)
		var b strings.Builder
		for i := range batch {
			batch[i].Alias = "c" + itoa(i+1)
			b.WriteString("[")
			b.WriteString(batch[i].Alias)
			b.WriteString("]\n<<<DOCUMENT>>>\n")
			b.WriteString(batch[i].Text)
			b.WriteString("\n<<<END>>>\n")
		}
		prompt := prompts.Render(prompts.GraphExtract, map[string]string{"chunks": b.String()})
		text, err := attemptCall(func() (string, error) {
			callCtx, cancel := context.WithTimeout(ctx, s.timeout())
			defer cancel()
			body, _, callErr := s.AI.Complete(callCtx, remote, prompt, "只输出 JSON")
			if callErr != nil {
				return "", callErr
			}
			if _, _, decErr := decodeExtraction(body); decErr != nil {
				return "", errUnparsed
			}
			return body, nil
		})
		if err != nil {
			_ = jobs.Stage(ctx, s.Pool, job.ID, "extract", "failed")
			if errors.Is(err, errUnparsed) {
				return s.fail(ctx, job.TargetID, "抽取结果无法解析")
			}
			return s.fail(ctx, job.TargetID, apperr.ErrModelDown.Message)
		}
		ents, rels, err := decodeExtraction(text)
		if err != nil {
			_ = jobs.Stage(ctx, s.Pool, job.ID, "extract", "failed")
			return s.fail(ctx, job.TargetID, "抽取结果无法解析")
		}
		accE, accR := ValidateExtraction(batch, ents, rels)
		allEnt = append(allEnt, accE...)
		allRel = append(allRel, accR...)
	}
	_ = jobs.Stage(ctx, s.Pool, job.ID, "extract", "succeeded")
	_ = jobs.Stage(ctx, s.Pool, job.ID, "write", "running")
	if err := s.apply(ctx, job, allEnt, allRel); err != nil {
		if errors.Is(err, jobs.ErrCancelled) {
			return err
		}
		_ = jobs.Stage(ctx, s.Pool, job.ID, "write", "failed")
		return s.fail(ctx, job.TargetID, "抽取结果无法解析")
	}
	_ = jobs.Stage(ctx, s.Pool, job.ID, "write", "succeeded")
	return nil
}

func (s Service) apply(ctx context.Context, job jobs.Job, ents []AcceptedEntity, rels []AcceptedRelation) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var title string
	var cancel bool
	var status string
	err = tx.QueryRow(ctx, `
		SELECT title, cancel_requested, status FROM documents WHERE id = $1 FOR UPDATE`,
		db.MustUUID(job.TargetID)).Scan(&title, &cancel, &status)
	if errors.Is(err, pgx.ErrNoRows) || cancel || status != "ready" {
		return jobs.ErrCancelled
	}
	if err != nil {
		return err
	}
	paperID, err := upsertEntity(ctx, tx, job.OwnerID, "paper", title, PaperKey(job.TargetID), job.TargetID)
	if err != nil {
		return err
	}
	ids := map[string]string{}
	for _, e := range ents {
		key := e.Type + ":" + NormalizeKey(e.Name)
		id, err := upsertEntity(ctx, tx, job.OwnerID, e.Type, e.Name, key, "")
		if err != nil {
			return err
		}
		ids[e.Type+":"+e.Name] = id
		ids[e.Type+":"+NormalizeKey(e.Name)] = id
	}
	if _, err := tx.Exec(ctx, `DELETE FROM kg_evidence WHERE document_id = $1`, db.MustUUID(job.TargetID)); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM kg_unresolved_refs WHERE document_id = $1`, db.MustUUID(job.TargetID)); err != nil {
		return err
	}
	titles, err := loadTitles(ctx, tx, job.OwnerID)
	if err != nil {
		return err
	}
	for _, rel := range rels {
		src, err := resolveEnd(ctx, tx, job, rel.Type, rel.Source, true, paperID, ids, titles)
		if err != nil {
			return err
		}
		tgt, unresolved, err := resolveTarget(ctx, tx, job, rel, paperID, ids, titles)
		if err != nil {
			return err
		}
		if unresolved {
			if _, err := tx.Exec(ctx, `
				INSERT INTO kg_unresolved_refs (id, document_id, title, created_at)
				VALUES ($1, $2, $3, now())`, db.MustUUID(db.NewID()), db.MustUUID(job.TargetID), strings.TrimSpace(rel.Target)); err != nil {
				return err
			}
			continue
		}
		if src == "" || tgt == "" {
			continue
		}
		var relID string
		err = tx.QueryRow(ctx, `
			INSERT INTO kg_relations (id, owner_id, type, source_id, target_id, suspended, created_at)
			VALUES ($1, $2, $3, $4, $5, false, now())
			ON CONFLICT (owner_id, type, source_id, target_id)
			DO UPDATE SET suspended = false
			RETURNING id::text`,
			db.MustUUID(db.NewID()), db.MustUUID(job.OwnerID), rel.Type, db.MustUUID(src), db.MustUUID(tgt)).Scan(&relID)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO kg_evidence (id, relation_id, document_id, chunk_id, chunk_version, page_start, sentence, created_at)
			VALUES ($1, $2, $3, $4, 1, $5, $6, now())`,
			db.MustUUID(db.NewID()), db.MustUUID(relID), db.MustUUID(job.TargetID), db.MustUUID(rel.ChunkID), rel.Page, rel.Sentence); err != nil {
			return err
		}
	}
	if err := dropEmpty(ctx, tx, job.OwnerID); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `
		UPDATE documents SET graph_status = 'ready', graph_failure_reason = NULL, updated_at = now()
		WHERE id = $1 AND cancel_requested = false AND status = 'ready'`, db.MustUUID(job.TargetID))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return jobs.ErrCancelled
	}
	return tx.Commit(ctx)
}

func (s Service) fail(ctx context.Context, documentID, reason string) error {
	_, err := s.Pool.Exec(ctx, `
		UPDATE documents SET graph_status = 'failed', graph_failure_reason = $2, updated_at = now()
		WHERE id = $1 AND status = 'ready'`, db.MustUUID(documentID), reason)
	if err != nil {
		return err
	}
	return errors.New(reason)
}

// SupplementScope 是由 knowledge.ResolvedScope 满足的最小只读范围约束，避免在 graph 与 knowledge 间引入包环依赖。
type SupplementScope interface {
	OwnerID() string
	GraphDocIDs() []string
}

// SupplementSQL 从种子切块作为证据的关系两端实体出发，沿一跳有效关系（含同关系共现与共享实体跨关系）
// 取额外证据切块，并严格限定在同一用户、已启用图谱的范围内、状态为 ready 的当前向量代际切块上。
const SupplementSQL = `
		WITH seed_relations AS (
			SELECT DISTINCT r.source_id, r.target_id
			FROM kg_evidence e
			JOIN kg_relations r ON r.id = e.relation_id
			JOIN chunks c ON c.id = e.chunk_id
			JOIN documents d ON d.id = c.document_id
			WHERE e.chunk_id = ANY($1::uuid[])
			  AND r.owner_id = $2
			  AND r.suspended = false
			  AND d.owner_id = $2
			  AND d.status = 'ready'
			  AND c.embedding_generation = $3
			  AND d.id = ANY($4::uuid[])
		),
		hit_entities AS (
			SELECT source_id AS entity_id FROM seed_relations
			UNION
			SELECT target_id AS entity_id FROM seed_relations
		),
		matched_relations AS (
			SELECT r2.id
			FROM kg_relations r2
			WHERE r2.owner_id = $2
			  AND r2.suspended = false
			  AND (
				  r2.source_id IN (SELECT entity_id FROM hit_entities)
				  OR r2.target_id IN (SELECT entity_id FROM hit_entities)
			  )
		)
		SELECT e2.chunk_id::text
		FROM matched_relations mr
		JOIN kg_evidence e2 ON e2.relation_id = mr.id
		JOIN chunks c ON c.id = e2.chunk_id
		JOIN documents d ON d.id = c.document_id
		WHERE d.owner_id = $2
		  AND d.status = 'ready'
		  AND c.embedding_generation = $3
		  AND d.id = ANY($4::uuid[])
		  AND NOT (e2.chunk_id = ANY($1::uuid[]))
		GROUP BY e2.chunk_id
		ORDER BY count(DISTINCT mr.id) DESC, e2.chunk_id::text
		LIMIT $5`

func (s Service) Supplement(ctx context.Context, scope SupplementScope, chunkIDs []string, generation int64, limit int) ([]string, error) {
	if scope == nil || len(chunkIDs) == 0 || limit <= 0 {
		return nil, nil
	}
	docIDs := scope.GraphDocIDs()
	if len(docIDs) == 0 {
		return nil, nil
	}
	ids, ok := db.UUIDs(chunkIDs)
	if !ok {
		return nil, nil
	}
	dUUIDs, ok := db.UUIDs(docIDs)
	if !ok {
		return nil, nil
	}
	ownerUUID, ok := db.UUID(scope.OwnerID())
	if !ok {
		return nil, nil
	}
	rows, err := s.Pool.Query(ctx, SupplementSQL, ids, ownerUUID, generation, dUUIDs, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	seen := map[string]bool{}
	for _, id := range chunkIDs {
		seen[id] = true
	}
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
		if len(out) >= limit {
			break
		}
	}
	return out, rows.Err()
}

func (s Service) timeout() time.Duration {
	if s.Timeout <= 0 {
		return 60 * time.Second
	}
	return s.Timeout
}

func decodeExtraction(text string) ([]RawEntity, []RawRelation, error) {
	text = strings.TrimSpace(text)
	if i := strings.Index(text, "{"); i >= 0 {
		if j := strings.LastIndex(text, "}"); j > i {
			text = text[i : j+1]
		}
	}
	var raw struct {
		Entities  []RawEntity   `json:"entities"`
		Relations []RawRelation `json:"relations"`
	}
	if err := json.Unmarshal([]byte(text), &raw); err != nil {
		return nil, nil, err
	}
	return raw.Entities, raw.Relations, nil
}

func upsertEntity(ctx context.Context, tx pgx.Tx, owner, typ, name, key, documentID string) (string, error) {
	var doc any
	if documentID != "" {
		doc = db.MustUUID(documentID)
	}
	var id string
	err := tx.QueryRow(ctx, `
		INSERT INTO kg_entities (id, owner_id, type, name, merge_key, document_id, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (owner_id, merge_key) DO UPDATE SET name = CASE WHEN kg_entities.type = 'paper' THEN EXCLUDED.name ELSE kg_entities.name END,
			document_id = COALESCE(EXCLUDED.document_id, kg_entities.document_id)
		RETURNING id::text`,
		db.MustUUID(db.NewID()), db.MustUUID(owner), typ, name, key, doc, time.Now().UTC()).Scan(&id)
	return id, err
}

func loadTitles(ctx context.Context, tx pgx.Tx, owner string) (map[string]string, error) {
	rows, err := tx.Query(ctx, `SELECT id::text, title FROM documents WHERE owner_id = $1 AND status = 'ready'`, db.MustUUID(owner))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, title string
		if err := rows.Scan(&id, &title); err != nil {
			return nil, err
		}
		out[NormalizeKey(title)] = id
	}
	return out, rows.Err()
}

func resolveEnd(ctx context.Context, tx pgx.Tx, job jobs.Job, relType, name string, source bool, paperID string, ids map[string]string, titles map[string]string) (string, error) {
	if name == "PAPER" {
		return paperID, nil
	}
	ends := relationEnds[relType]
	typ := ends[1]
	if source {
		typ = ends[0]
	}
	if typ == "paper" && !source {
		return "", nil
	}
	if id, ok := ids[typ+":"+name]; ok {
		return id, nil
	}
	if id, ok := ids[typ+":"+NormalizeKey(name)]; ok {
		return id, nil
	}
	return "", nil
}

func resolveTarget(ctx context.Context, tx pgx.Tx, job jobs.Job, rel AcceptedRelation, paperID string, ids map[string]string, titles map[string]string) (string, bool, error) {
	if rel.Target == "PAPER" {
		return paperID, false, nil
	}
	if rel.Cites || rel.Type == "cites" {
		if docID, ok := titles[NormalizeKey(rel.Target)]; ok {
			id, err := upsertEntity(ctx, tx, job.OwnerID, "paper", strings.TrimSpace(rel.Target), PaperKey(docID), docID)
			return id, false, err
		}
		return "", true, nil
	}
	id, err := resolveEnd(ctx, tx, job, rel.Type, rel.Target, false, paperID, ids, titles)
	return id, false, err
}

func Cleanup(ctx context.Context, tx pgx.Tx, owner string) error {
	return dropEmpty(ctx, tx, owner)
}

func dropEmpty(ctx context.Context, tx pgx.Tx, owner string) error {
	if _, err := tx.Exec(ctx, `
		DELETE FROM kg_relations r
		WHERE r.owner_id = $1
		  AND NOT EXISTS (SELECT 1 FROM kg_evidence e WHERE e.relation_id = r.id)`, db.MustUUID(owner)); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `
		DELETE FROM kg_entities e
		WHERE e.owner_id = $1
		  AND NOT EXISTS (SELECT 1 FROM kg_relations r WHERE r.source_id = e.id OR r.target_id = e.id)
		  AND (e.document_id IS NULL OR NOT EXISTS (SELECT 1 FROM documents d WHERE d.id = e.document_id))`,
		db.MustUUID(owner))
	return err
}

func nullUUID(s string) any {
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

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [12]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
