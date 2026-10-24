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
	Pool *pgxpool.Pool
	AI   provider.Client
	Models provider.Store
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
		callCtx, cancel := context.WithTimeout(ctx, s.timeout())
		text, _, err := s.AI.Complete(callCtx, remote, prompts.Render(prompts.GraphExtract, map[string]string{"chunks": b.String()}), "只输出 JSON")
		cancel()
		if err != nil {
			_ = jobs.Stage(ctx, s.Pool, job.ID, "extract", "failed")
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

func (s Service) Supplement(ctx context.Context, owner string, chunkIDs []string, generation int64, limit int) ([]string, error) {
	if len(chunkIDs) == 0 || limit <= 0 {
		return nil, nil
	}
	ids, ok := db.UUIDs(chunkIDs)
	if !ok {
		return nil, nil
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT DISTINCT e2.chunk_id::text
		FROM kg_evidence e
		JOIN kg_relations r ON r.id = e.relation_id AND r.suspended = false
		JOIN kg_evidence e2 ON e2.relation_id = r.id AND e2.chunk_id <> e.chunk_id
		JOIN chunks c ON c.id = e2.chunk_id
		JOIN documents d ON d.id = c.document_id
		WHERE e.chunk_id = ANY($1::uuid[])
		  AND d.owner_id = $2
		  AND d.status = 'ready'
		  AND c.embedding_generation = $3
		LIMIT $4`, ids, db.MustUUID(owner), generation, limit+len(chunkIDs))
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

type Filter struct {
	KBID        string
	DocumentIDs []string
	TagIDs      []string
	Limit       int
}

func (s Service) Query(ctx context.Context, owner string, f Filter) (View, error) {
	if f.Limit <= 0 {
		f.Limit = 300
	}
	docs, err := s.scopeDocs(ctx, owner, f)
	if err != nil {
		return View{}, err
	}
	if docs == nil {
		docs = []string{}
	}
	view := View{Nodes: []Node{}, Edges: []Edge{}}
	if len(docs) == 0 && (f.KBID != "" || len(f.DocumentIDs) > 0 || len(f.TagIDs) > 0) {
		return view, nil
	}
	nodeSQL := `
		SELECT e.id::text, e.name, e.type, e.document_id::text
		FROM kg_entities e
		WHERE e.owner_id = $1
		  AND (
		    e.document_id = ANY($2::uuid[])
		    OR e.id IN (
		      SELECT r.source_id FROM kg_relations r
		      JOIN kg_evidence ev ON ev.relation_id = r.id
		      WHERE ev.document_id = ANY($2::uuid[])
		      UNION
		      SELECT r.target_id FROM kg_relations r
		      JOIN kg_evidence ev ON ev.relation_id = r.id
		      WHERE ev.document_id = ANY($2::uuid[])
		    )
		  )`
	args := []any{db.MustUUID(owner), uuidSlice(docs)}
	if len(docs) == 0 {
		nodeSQL = `
			SELECT e.id::text, e.name, e.type, e.document_id::text
			FROM kg_entities e WHERE e.owner_id = $1`
		args = []any{db.MustUUID(owner)}
	}
	rows, err := s.Pool.Query(ctx, nodeSQL, args...)
	if err != nil {
		return View{}, err
	}
	defer rows.Close()
	var nodes []Node
	for rows.Next() {
		var n Node
		var doc *string
		if err := rows.Scan(&n.ID, &n.Name, &n.Type, &doc); err != nil {
			return View{}, err
		}
		n.DocumentID = doc
		nodes = append(nodes, n)
	}
	if err := rows.Err(); err != nil {
		return View{}, err
	}
	if len(nodes) > f.Limit {
		view.Truncated = true
		nodes = nodes[:f.Limit]
	}
	if nodes == nil {
		nodes = []Node{}
	}
	view.Nodes = nodes
	if len(nodes) == 0 {
		return view, nil
	}
	nodeIDs := make([]string, len(nodes))
	for i := range nodes {
		nodeIDs[i] = nodes[i].ID
	}
	uids, _ := db.UUIDs(nodeIDs)
	erows, err := s.Pool.Query(ctx, `
		SELECT id::text, type, source_id::text, target_id::text, suspended
		FROM kg_relations
		WHERE owner_id = $1 AND source_id = ANY($2::uuid[]) AND target_id = ANY($2::uuid[])`,
		db.MustUUID(owner), uids)
	if err != nil {
		return View{}, err
	}
	defer erows.Close()
	for erows.Next() {
		var e Edge
		if err := erows.Scan(&e.ID, &e.Type, &e.Source, &e.Target, &e.Suspended); err != nil {
			return View{}, err
		}
		view.Edges = append(view.Edges, e)
	}
	if view.Edges == nil {
		view.Edges = []Edge{}
	}
	return view, erows.Err()
}

func (s Service) scopeDocs(ctx context.Context, owner string, f Filter) ([]string, error) {
	if f.KBID != "" {
		if _, ok := db.UUID(f.KBID); !ok {
			return []string{}, nil
		}
	}
	if _, ok := db.UUIDs(f.DocumentIDs); !ok {
		return []string{}, nil
	}
	if _, ok := db.UUIDs(f.TagIDs); !ok {
		return []string{}, nil
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT d.id::text FROM documents d
		WHERE d.owner_id = $1
		  AND ($2::uuid IS NULL OR d.kb_id = $2)
		  AND (cardinality($3::uuid[]) = 0 OR d.id = ANY($3::uuid[]))
		  AND (cardinality($4::uuid[]) = 0 OR EXISTS (
		    SELECT 1 FROM document_tags t WHERE t.document_id = d.id AND t.tag_id = ANY($4::uuid[])
		  ))`, db.MustUUID(owner), nullUUID(f.KBID), uuidSlice(f.DocumentIDs), uuidSlice(f.TagIDs))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func (s Service) DocumentHop(ctx context.Context, owner, documentID string) (View, error) {
	return s.Query(ctx, owner, Filter{DocumentIDs: []string{documentID}, Limit: 300})
}

type Evidence struct {
	DocumentID string `json:"document_id"`
	ChunkID    string `json:"chunk_id"`
	PageStart  int    `json:"page_start"`
	Sentence   string `json:"sentence"`
	Label      string `json:"label"`
}

func (s Service) Node(ctx context.Context, owner, id string) (map[string]any, error) {
	var name, typ string
	var doc *string
	err := s.Pool.QueryRow(ctx, `
		SELECT name, type, document_id::text FROM kg_entities WHERE id = $1 AND owner_id = $2`,
		db.MustUUID(id), db.MustUUID(owner)).Scan(&name, &typ, &doc)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, apperr.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT ev.document_id::text, ev.chunk_id::text, ev.page_start, ev.sentence, d.title
		FROM kg_evidence ev
		JOIN kg_relations r ON r.id = ev.relation_id
		JOIN documents d ON d.id = ev.document_id
		WHERE (r.source_id = $1 OR r.target_id = $1) AND r.owner_id = $2
		ORDER BY ev.created_at
		LIMIT 20`, db.MustUUID(id), db.MustUUID(owner))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var pages []map[string]any
	for rows.Next() {
		var docID, chunkID, title, sentence string
		var page int
		if err := rows.Scan(&docID, &chunkID, &page, &sentence, &title); err != nil {
			return nil, err
		}
		pages = append(pages, map[string]any{
			"document_id": docID, "title": title, "page_start": page, "chunk_id": chunkID, "sentence": sentence,
		})
	}
	if pages == nil {
		pages = []map[string]any{}
	}
	return map[string]any{"id": id, "name": name, "type": typ, "document_id": doc, "sources": pages}, rows.Err()
}

func (s Service) Edge(ctx context.Context, owner, id string) (map[string]any, error) {
	var typ, src, tgt string
	err := s.Pool.QueryRow(ctx, `
		SELECT type, source_id::text, target_id::text FROM kg_relations WHERE id = $1 AND owner_id = $2`,
		db.MustUUID(id), db.MustUUID(owner)).Scan(&typ, &src, &tgt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, apperr.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT document_id::text, chunk_id::text, page_start, sentence
		FROM kg_evidence WHERE relation_id = $1 ORDER BY created_at`, db.MustUUID(id))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ev []Evidence
	for rows.Next() {
		var e Evidence
		if err := rows.Scan(&e.DocumentID, &e.ChunkID, &e.PageStart, &e.Sentence); err != nil {
			return nil, err
		}
		ev = append(ev, e)
	}
	if ev == nil {
		ev = []Evidence{}
	}
	return map[string]any{"id": id, "type": typ, "source": src, "target": tgt, "evidence": ev}, rows.Err()
}

func (s Service) Unresolved(ctx context.Context, owner, documentID string) ([]string, error) {
	var exists bool
	err := s.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM documents WHERE id = $1 AND owner_id = $2)`,
		db.MustUUID(documentID), db.MustUUID(owner)).Scan(&exists)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, apperr.ErrNotFound
	}
	rows, err := s.Pool.Query(ctx, `SELECT title FROM kg_unresolved_refs WHERE document_id = $1 ORDER BY created_at`, db.MustUUID(documentID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var title string
		if err := rows.Scan(&title); err != nil {
			return nil, err
		}
		out = append(out, title)
	}
	return out, rows.Err()
}

func (s Service) State(ctx context.Context, owner, documentID string) (map[string]any, error) {
	var status *string
	var reason *string
	var retries int
	err := s.Pool.QueryRow(ctx, `
		SELECT graph_status, graph_failure_reason, graph_manual_retry_count
		FROM documents WHERE id = $1 AND owner_id = $2`, db.MustUUID(documentID), db.MustUUID(owner)).Scan(&status, &reason, &retries)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, apperr.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	code := ""
	if status != nil {
		code = *status
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT js.name, js.status FROM job_stages js
		JOIN jobs j ON j.id = js.job_id
		WHERE j.target_id = $1 AND j.kind = 'graph_extract'
		ORDER BY j.created_at DESC, js.started_at
		LIMIT 10`, db.MustUUID(documentID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	stages := []map[string]string{}
	for rows.Next() {
		var name, st string
		if err := rows.Scan(&name, &st); err != nil {
			return nil, err
		}
		stages = append(stages, map[string]string{"name": name, "status": st})
	}
	return map[string]any{
		"graph_status": identityLabel(code), "failure_reason": reason,
		"manual_retry_count": retries, "stages": stages,
	}, rows.Err()
}

func identityLabel(code string) any {
	if code == "" {
		return nil
	}
	switch code {
	case "pending":
		return "待抽取"
	case "extracting":
		return "抽取中"
	case "ready":
		return "已完成"
	case "failed":
		return "失败"
	default:
		return code
	}
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
