package graph

import (
	"context"
	"errors"
	"sort"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"yanzhi/server/internal/apperr"
	"yanzhi/server/internal/identity"
	"yanzhi/server/internal/platform/db"
)

const defaultBrowseLimit = 300

// LibraryFilter scopes the library graph. It is not a hop.
// Named DocumentIDs are an open: any missing, foreign, or malformed id is NotFound.
// KBID and TagIDs are filters: a miss is an empty View.
// limit is unexported so HTTP cannot set it. Zero means defaultBrowseLimit.
type LibraryFilter struct {
	KBID        string
	DocumentIDs []string
	TagIDs      []string
	limit       int
}

type SourcePage struct {
	DocumentID string `json:"document_id"`
	Title      string `json:"title"`
	PageStart  int    `json:"page_start"`
}

type NodeDetail struct {
	ID         string       `json:"id"`
	Name       string       `json:"name"`
	Type       string       `json:"type"`
	DocumentID *string      `json:"document_id"`
	Sources    []SourcePage `json:"sources"`
}

type Evidence struct {
	DocumentID string `json:"document_id"`
	ChunkID    string `json:"chunk_id"`
	PageStart  int    `json:"page_start"`
	Sentence   string `json:"sentence"`
}

type EdgeDetail struct {
	ID       string     `json:"id"`
	Type     string     `json:"type"`
	Source   string     `json:"source"`
	Target   string     `json:"target"`
	Evidence []Evidence `json:"evidence"`
}

type Stage struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

type Extraction struct {
	GraphStatus      *string `json:"graph_status"`
	FailureReason    *string `json:"failure_reason"`
	ManualRetryCount int     `json:"manual_retry_count"`
	Stages           []Stage `json:"stages"`
}

func emptyView() View {
	return View{Nodes: []Node{}, Edges: []Edge{}}
}

func statusLabel(code string) *string {
	label := identity.GraphStatusLabel(code)
	text, ok := label.(string)
	if !ok || text == "" {
		return nil
	}
	return &text
}

func resolvedLimit(n int) int {
	if n <= 0 {
		return defaultBrowseLimit
	}
	return n
}

func limitView(nodes []Node, edges []Edge, limit int, pin string) View {
	limit = resolvedLimit(limit)
	byID := make(map[string]Node, len(nodes))
	ids := make([]string, 0, len(nodes))
	for _, n := range nodes {
		if _, ok := byID[n.ID]; ok {
			continue
		}
		byID[n.ID] = n
		ids = append(ids, n.ID)
	}
	ranked := rankByDegree(ids, edges)
	truncated := len(ranked) > limit
	kept := ranked
	if pin != "" {
		if _, ok := byID[pin]; ok {
			others := make([]string, 0, len(ranked))
			for _, id := range ranked {
				if id != pin {
					others = append(others, id)
				}
			}
			room := limit - 1
			if room < 0 {
				room = 0
			}
			if len(others) > room {
				others = others[:room]
			}
			kept = append([]string{pin}, others...)
		} else if len(kept) > limit {
			kept = kept[:limit]
		}
	} else if len(kept) > limit {
		kept = kept[:limit]
	}
	keptSet := make(map[string]struct{}, len(kept))
	outNodes := make([]Node, 0, len(kept))
	for _, id := range kept {
		keptSet[id] = struct{}{}
		outNodes = append(outNodes, byID[id])
	}
	outEdges := make([]Edge, 0)
	for _, e := range edges {
		if _, ok := keptSet[e.Source]; !ok {
			continue
		}
		if _, ok := keptSet[e.Target]; !ok {
			continue
		}
		if pin != "" && e.Source != pin && e.Target != pin {
			continue
		}
		outEdges = append(outEdges, e)
	}
	return View{Nodes: outNodes, Edges: outEdges, Truncated: truncated}
}

func rankByDegree(ids []string, edges []Edge) []string {
	set := make(map[string]struct{}, len(ids))
	deg := make(map[string]int, len(ids))
	for _, id := range ids {
		set[id] = struct{}{}
		deg[id] = 0
	}
	for _, e := range edges {
		if _, ok := set[e.Source]; !ok {
			continue
		}
		if _, ok := set[e.Target]; !ok {
			continue
		}
		if e.Source == e.Target {
			deg[e.Source]++
			continue
		}
		deg[e.Source]++
		deg[e.Target]++
	}
	ranked := append([]string(nil), ids...)
	sort.Slice(ranked, func(i, j int) bool {
		if deg[ranked[i]] != deg[ranked[j]] {
			return deg[ranked[i]] > deg[ranked[j]]
		}
		return ranked[i] < ranked[j]
	})
	return ranked
}

func (s Service) Subgraph(ctx context.Context, owner string, f LibraryFilter) (View, error) {
	limit := resolvedLimit(f.limit)
	ownerID, ok := db.UUID(owner)
	if !ok {
		return View{}, apperr.ErrNotFound
	}
	filtered := f.KBID != "" || len(f.DocumentIDs) > 0 || len(f.TagIDs) > 0
	if len(f.DocumentIDs) > 0 {
		ids, ok := canonicalUUIDs(f.DocumentIDs)
		if !ok {
			return View{}, apperr.ErrNotFound
		}
		var n int64
		err := s.Pool.QueryRow(ctx, `
			SELECT count(*) FROM documents WHERE owner_id = $1 AND id = ANY($2::uuid[])`,
			ownerID, uuidSlice(ids)).Scan(&n)
		if err != nil {
			return View{}, err
		}
		if n != int64(len(ids)) {
			return View{}, apperr.ErrNotFound
		}
		f.DocumentIDs = ids
	}
	var docs []string
	if filtered {
		var err error
		docs, err = s.scopeDocs(ctx, ownerID, f)
		if err != nil {
			return View{}, err
		}
		if len(docs) == 0 {
			return emptyView(), nil
		}
	}
	nodes, err := s.loadNodes(ctx, ownerID, docs, filtered)
	if err != nil {
		return View{}, err
	}
	if len(nodes) == 0 {
		return emptyView(), nil
	}
	ids := make([]string, len(nodes))
	for i := range nodes {
		ids[i] = nodes[i].ID
	}
	edges, err := s.edgesAmong(ctx, ownerID, ids)
	if err != nil {
		return View{}, err
	}
	return limitView(nodes, edges, limit, ""), nil
}

func (s Service) Neighbors(ctx context.Context, owner, documentID string) (View, error) {
	if err := s.ownedDocument(ctx, owner, documentID); err != nil {
		return View{}, err
	}
	ownerID, ok := db.UUID(owner)
	if !ok {
		return View{}, apperr.ErrNotFound
	}
	var paper Node
	var doc *string
	err := s.Pool.QueryRow(ctx, `
		SELECT id::text, name, type, document_id::text
		FROM kg_entities
		WHERE owner_id = $1 AND merge_key = $2`,
		ownerID, PaperKey(documentID)).Scan(&paper.ID, &paper.Name, &paper.Type, &doc)
	if errors.Is(err, pgx.ErrNoRows) {
		return emptyView(), nil
	}
	if err != nil {
		return View{}, err
	}
	paper.DocumentID = doc
	paperUUID, ok := db.UUID(paper.ID)
	if !ok {
		return View{}, errors.New("invalid paper id")
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT CASE WHEN source_id = $2 THEN target_id::text ELSE source_id::text END
		FROM kg_relations
		WHERE owner_id = $1 AND (source_id = $2 OR target_id = $2)`, ownerID, paperUUID)
	if err != nil {
		return View{}, err
	}
	defer rows.Close()
	seen := map[string]struct{}{paper.ID: {}}
	var neighborIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return View{}, err
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		neighborIDs = append(neighborIDs, id)
	}
	if err := rows.Err(); err != nil {
		return View{}, err
	}
	neighbors := []Node{}
	if len(neighborIDs) > 0 {
		nrows, err := s.Pool.Query(ctx, `
			SELECT id::text, name, type, document_id::text
			FROM kg_entities WHERE owner_id = $1 AND id = ANY($2::uuid[])`, ownerID, uuidSlice(neighborIDs))
		if err != nil {
			return View{}, err
		}
		defer nrows.Close()
		neighbors, err = scanNodes(nrows)
		if err != nil {
			return View{}, err
		}
	}
	nodes := make([]Node, 0, 1+len(neighbors))
	nodes = append(nodes, paper)
	nodes = append(nodes, neighbors...)
	ids := make([]string, len(nodes))
	for i := range nodes {
		ids[i] = nodes[i].ID
	}
	edges, err := s.edgesAmong(ctx, ownerID, ids)
	if err != nil {
		return View{}, err
	}
	return limitView(nodes, edges, defaultBrowseLimit, paper.ID), nil
}

func (s Service) Node(ctx context.Context, owner, id string) (NodeDetail, error) {
	ownerID, nodeID, err := ownerAndID(owner, id)
	if err != nil {
		return NodeDetail{}, err
	}
	if err := s.ownedRow(ctx, `SELECT EXISTS(SELECT 1 FROM kg_entities WHERE id = $1 AND owner_id = $2)`, nodeID, ownerID); err != nil {
		return NodeDetail{}, err
	}
	var detail NodeDetail
	var doc *string
	err = s.Pool.QueryRow(ctx, `
		SELECT id::text, name, type, document_id::text
		FROM kg_entities WHERE id = $1 AND owner_id = $2`, nodeID, ownerID).Scan(&detail.ID, &detail.Name, &detail.Type, &doc)
	if errors.Is(err, pgx.ErrNoRows) {
		return NodeDetail{}, apperr.ErrNotFound
	}
	if err != nil {
		return NodeDetail{}, err
	}
	detail.DocumentID = doc
	rows, err := s.Pool.Query(ctx, `
		SELECT DISTINCT ev.document_id::text, d.title, ev.page_start
		FROM kg_evidence ev
		JOIN kg_relations r ON r.id = ev.relation_id
		JOIN documents d ON d.id = ev.document_id
		WHERE r.owner_id = $1 AND (r.source_id = $2 OR r.target_id = $2)
		ORDER BY d.title, ev.page_start`, ownerID, nodeID)
	if err != nil {
		return NodeDetail{}, err
	}
	defer rows.Close()
	detail.Sources = []SourcePage{}
	for rows.Next() {
		var page SourcePage
		if err := rows.Scan(&page.DocumentID, &page.Title, &page.PageStart); err != nil {
			return NodeDetail{}, err
		}
		detail.Sources = append(detail.Sources, page)
	}
	return detail, rows.Err()
}

func (s Service) Edge(ctx context.Context, owner, id string) (EdgeDetail, error) {
	ownerID, edgeID, err := ownerAndID(owner, id)
	if err != nil {
		return EdgeDetail{}, err
	}
	if err := s.ownedRow(ctx, `SELECT EXISTS(SELECT 1 FROM kg_relations WHERE id = $1 AND owner_id = $2)`, edgeID, ownerID); err != nil {
		return EdgeDetail{}, err
	}
	var detail EdgeDetail
	err = s.Pool.QueryRow(ctx, `
		SELECT id::text, type, source_id::text, target_id::text
		FROM kg_relations WHERE id = $1 AND owner_id = $2`, edgeID, ownerID).Scan(&detail.ID, &detail.Type, &detail.Source, &detail.Target)
	if errors.Is(err, pgx.ErrNoRows) {
		return EdgeDetail{}, apperr.ErrNotFound
	}
	if err != nil {
		return EdgeDetail{}, err
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT document_id::text, chunk_id::text, page_start, sentence
		FROM kg_evidence WHERE relation_id = $1 ORDER BY created_at`, edgeID)
	if err != nil {
		return EdgeDetail{}, err
	}
	defer rows.Close()
	detail.Evidence = []Evidence{}
	for rows.Next() {
		var ev Evidence
		if err := rows.Scan(&ev.DocumentID, &ev.ChunkID, &ev.PageStart, &ev.Sentence); err != nil {
			return EdgeDetail{}, err
		}
		detail.Evidence = append(detail.Evidence, ev)
	}
	return detail, rows.Err()
}

func (s Service) Extraction(ctx context.Context, owner, documentID string) (Extraction, error) {
	if err := s.ownedDocument(ctx, owner, documentID); err != nil {
		return Extraction{}, err
	}
	var status *string
	var reason *string
	var retries int
	err := s.Pool.QueryRow(ctx, `
		SELECT graph_status, graph_failure_reason, graph_manual_retry_count
		FROM documents WHERE id = $1 AND owner_id = $2`, db.MustUUID(documentID), db.MustUUID(owner)).Scan(&status, &reason, &retries)
	if errors.Is(err, pgx.ErrNoRows) {
		return Extraction{}, apperr.ErrNotFound
	}
	if err != nil {
		return Extraction{}, err
	}
	code := ""
	if status != nil {
		code = *status
	}
	out := Extraction{
		GraphStatus:      statusLabel(code),
		FailureReason:    reason,
		ManualRetryCount: retries,
		Stages:           []Stage{},
	}
	var jobID string
	err = s.Pool.QueryRow(ctx, `
		SELECT id::text FROM jobs
		WHERE target_id = $1 AND kind = 'graph_extract'
		ORDER BY created_at DESC LIMIT 1`, db.MustUUID(documentID)).Scan(&jobID)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return Extraction{}, err
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT name, status FROM job_stages
		WHERE job_id = $1 ORDER BY started_at, name`, db.MustUUID(jobID))
	if err != nil {
		return Extraction{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var stage Stage
		if err := rows.Scan(&stage.Name, &stage.Status); err != nil {
			return Extraction{}, err
		}
		out.Stages = append(out.Stages, stage)
	}
	return out, rows.Err()
}

func (s Service) Unresolved(ctx context.Context, owner, documentID string) ([]string, error) {
	if err := s.ownedDocument(ctx, owner, documentID); err != nil {
		return nil, err
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT title FROM kg_unresolved_refs WHERE document_id = $1 ORDER BY created_at`, db.MustUUID(documentID))
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

func (s Service) ownedDocument(ctx context.Context, owner, id string) error {
	ownerID, docID, err := ownerAndID(owner, id)
	if err != nil {
		return err
	}
	return s.ownedRow(ctx, `SELECT EXISTS(SELECT 1 FROM documents WHERE id = $1 AND owner_id = $2)`, docID, ownerID)
}

func (s Service) ownedRow(ctx context.Context, sql string, id, owner pgtype.UUID) error {
	var exists bool
	if err := s.Pool.QueryRow(ctx, sql, id, owner).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return apperr.ErrNotFound
	}
	return nil
}

func ownerAndID(owner, id string) (pgtype.UUID, pgtype.UUID, error) {
	ownerID, ok := db.UUID(owner)
	if !ok {
		return pgtype.UUID{}, pgtype.UUID{}, apperr.ErrNotFound
	}
	rowID, ok := db.UUID(id)
	if !ok {
		return pgtype.UUID{}, pgtype.UUID{}, apperr.ErrNotFound
	}
	return ownerID, rowID, nil
}

func (s Service) scopeDocs(ctx context.Context, owner pgtype.UUID, f LibraryFilter) ([]string, error) {
	if f.KBID != "" {
		if _, ok := db.UUID(f.KBID); !ok {
			return []string{}, nil
		}
	}
	if len(f.TagIDs) > 0 {
		if _, ok := db.UUIDs(f.TagIDs); !ok {
			return []string{}, nil
		}
	}
	if len(f.DocumentIDs) > 0 {
		if _, ok := db.UUIDs(f.DocumentIDs); !ok {
			return nil, apperr.ErrNotFound
		}
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT d.id::text FROM documents d
		WHERE d.owner_id = $1
		  AND ($2::uuid IS NULL OR d.kb_id = $2)
		  AND (cardinality($3::uuid[]) = 0 OR d.id = ANY($3::uuid[]))
		  AND (cardinality($4::uuid[]) = 0 OR EXISTS (
		    SELECT 1 FROM document_tags t WHERE t.document_id = d.id AND t.tag_id = ANY($4::uuid[])
		  ))`, owner, nullUUID(f.KBID), uuidSlice(f.DocumentIDs), uuidSlice(f.TagIDs))
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

func (s Service) loadNodes(ctx context.Context, owner pgtype.UUID, docs []string, filtered bool) ([]Node, error) {
	var rows pgx.Rows
	var err error
	if !filtered {
		rows, err = s.Pool.Query(ctx, `
			SELECT e.id::text, e.name, e.type, e.document_id::text
			FROM kg_entities e WHERE e.owner_id = $1`, owner)
	} else {
		rows, err = s.Pool.Query(ctx, `
			SELECT e.id::text, e.name, e.type, e.document_id::text
			FROM kg_entities e
			WHERE e.owner_id = $1
			  AND (
			    e.document_id = ANY($2::uuid[])
			    OR EXISTS (
			      SELECT 1 FROM kg_relations r
			      JOIN kg_evidence ev ON ev.relation_id = r.id
			      WHERE r.owner_id = $1
			        AND ev.document_id = ANY($2::uuid[])
			        AND (r.source_id = e.id OR r.target_id = e.id)
			    )
			  )`, owner, uuidSlice(docs))
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanNodes(rows)
}

func (s Service) edgesAmong(ctx context.Context, owner pgtype.UUID, ids []string) ([]Edge, error) {
	if len(ids) == 0 {
		return []Edge{}, nil
	}
	uids, ok := db.UUIDs(ids)
	if !ok {
		return nil, errors.New("invalid entity id")
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT id::text, type, source_id::text, target_id::text, suspended
		FROM kg_relations
		WHERE owner_id = $1 AND source_id = ANY($2::uuid[]) AND target_id = ANY($2::uuid[])`, owner, uids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Edge{}
	for rows.Next() {
		var e Edge
		if err := rows.Scan(&e.ID, &e.Type, &e.Source, &e.Target, &e.Suspended); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func scanNodes(rows pgx.Rows) ([]Node, error) {
	nodes := []Node{}
	for rows.Next() {
		var n Node
		var doc *string
		if err := rows.Scan(&n.ID, &n.Name, &n.Type, &doc); err != nil {
			return nil, err
		}
		n.DocumentID = doc
		nodes = append(nodes, n)
	}
	return nodes, rows.Err()
}

func canonicalUUIDs(ids []string) ([]string, bool) {
	seen := make(map[string]struct{}, len(ids))
	out := make([]string, 0, len(ids))
	for _, raw := range ids {
		u, err := uuid.Parse(raw)
		if err != nil {
			return nil, false
		}
		id := u.String()
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out, true
}
