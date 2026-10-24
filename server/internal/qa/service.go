package qa

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"yanzhi/server/internal/apperr"
	"yanzhi/server/internal/graph"
	"yanzhi/server/internal/platform/clock"
	"yanzhi/server/internal/platform/cryptobox"
	"yanzhi/server/internal/platform/db"
	"yanzhi/server/internal/prompts"
	"yanzhi/server/internal/provider"
	"yanzhi/server/internal/retrieval"
)

type Stops struct {
	mu sync.Mutex
	m  map[string]bool
}

func NewStops() *Stops { return &Stops{m: map[string]bool{}} }

func (s *Stops) Begin(id string) {
	s.mu.Lock()
	delete(s.m, id)
	s.mu.Unlock()
}

func (s *Stops) Request(id string) {
	s.mu.Lock()
	s.m[id] = true
	s.mu.Unlock()
}

func (s *Stops) On(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.m[id]
}

type Service struct {
	Pool      *pgxpool.Pool
	AI        provider.Client
	Models    provider.Store
	Graph     graph.Service
	TopK      int
	RRFK      int
	K1        float64
	B         float64
	Threshold float64
	Stops     *Stops
	Timeout   time.Duration
}

type Session struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
	Turns     []Turn `json:"turns,omitempty"`
}

type Turn struct {
	ID         string     `json:"id"`
	Question   string     `json:"question"`
	Answer     string     `json:"answer"`
	AnswerKind string     `json:"answer_kind"`
	CreatedAt  string     `json:"created_at"`
	Citations  []Citation `json:"citations"`
}

type Citation struct {
	ChunkID         *string `json:"chunk_id"`
	DocumentID      *string `json:"document_id"`
	Label           string  `json:"label"`
	Origin          string  `json:"origin"`
	GraphSupplement bool    `json:"graph_supplement"`
}

type Scope struct {
	Type        string   `json:"type"`
	KBID        string   `json:"kb_id"`
	TagIDs      []string `json:"tag_ids"`
	DocumentIDs []string `json:"document_ids"`
}

func (s *Service) CreateSession(ctx context.Context, owner, title string) (Session, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		title = "新会话"
	}
	if len([]rune(title)) > 80 {
		return Session{}, apperr.WithDetails("VALIDATION_ERROR", "标题不能超过 80 个字符", map[string]any{"field": "title"})
	}
	id := db.NewID()
	var created, updated time.Time
	err := s.Pool.QueryRow(ctx, `
		INSERT INTO qa_sessions (id, owner_id, title, created_at, updated_at)
		VALUES ($1, $2, $3, now(), now()) RETURNING created_at, updated_at`,
		db.MustUUID(id), db.MustUUID(owner), title).Scan(&created, &updated)
	if err != nil {
		return Session{}, err
	}
	return Session{ID: id, Title: title, CreatedAt: clock.Format(created), UpdatedAt: clock.Format(updated)}, nil
}

func (s *Service) ListSessions(ctx context.Context, owner string, page, size int) ([]Session, int, error) {
	if page < 1 {
		page = 1
	}
	if size < 1 || size > 100 {
		size = 20
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT id::text, title, created_at, updated_at FROM qa_sessions
		WHERE owner_id = $1 ORDER BY updated_at DESC LIMIT $2 OFFSET $3`,
		db.MustUUID(owner), size, (page-1)*size)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []Session{}
	for rows.Next() {
		var item Session
		var created, updated time.Time
		if err := rows.Scan(&item.ID, &item.Title, &created, &updated); err != nil {
			return nil, 0, err
		}
		item.CreatedAt = clock.Format(created)
		item.UpdatedAt = clock.Format(updated)
		out = append(out, item)
	}
	var total int
	err = s.Pool.QueryRow(ctx, `SELECT count(*) FROM qa_sessions WHERE owner_id = $1`, db.MustUUID(owner)).Scan(&total)
	return out, total, err
}

func (s *Service) GetSession(ctx context.Context, owner, id string) (Session, error) {
	var item Session
	var created, updated time.Time
	err := s.Pool.QueryRow(ctx, `
		SELECT id::text, title, created_at, updated_at FROM qa_sessions WHERE id = $1 AND owner_id = $2`,
		db.MustUUID(id), db.MustUUID(owner)).Scan(&item.ID, &item.Title, &created, &updated)
	if errors.Is(err, pgx.ErrNoRows) {
		return Session{}, apperr.ErrNotFound
	}
	if err != nil {
		return Session{}, err
	}
	item.CreatedAt = clock.Format(created)
	item.UpdatedAt = clock.Format(updated)
	rows, err := s.Pool.Query(ctx, `
		SELECT id::text, question, answer, answer_kind, created_at FROM qa_turns
		WHERE session_id = $1 ORDER BY created_at`, db.MustUUID(id))
	if err != nil {
		return Session{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var t Turn
		var at time.Time
		if err := rows.Scan(&t.ID, &t.Question, &t.Answer, &t.AnswerKind, &at); err != nil {
			return Session{}, err
		}
		t.CreatedAt = clock.Format(at)
		t.Citations = []Citation{}
		item.Turns = append(item.Turns, t)
	}
	if item.Turns == nil {
		item.Turns = []Turn{}
	}
	if err := rows.Err(); err != nil {
		return Session{}, err
	}
	for i := range item.Turns {
		cites, err := s.citations(ctx, item.Turns[i].ID)
		if err != nil {
			return Session{}, err
		}
		item.Turns[i].Citations = cites
	}
	return item, nil
}

func (s *Service) citations(ctx context.Context, turnID string) ([]Citation, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT chunk_id::text, document_id::text, label, origin, graph_supplement
		FROM qa_citations WHERE turn_id = $1 ORDER BY id`, db.MustUUID(turnID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Citation{}
	for rows.Next() {
		var c Citation
		if err := rows.Scan(&c.ChunkID, &c.DocumentID, &c.Label, &c.Origin, &c.GraphSupplement); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Service) PatchSession(ctx context.Context, owner, id, title string) (Session, error) {
	title = strings.TrimSpace(title)
	if title == "" || len([]rune(title)) > 80 {
		return Session{}, apperr.WithDetails("VALIDATION_ERROR", "标题需要 1 到 80 个字符", map[string]any{"field": "title"})
	}
	tag, err := s.Pool.Exec(ctx, `UPDATE qa_sessions SET title = $3, updated_at = now() WHERE id = $1 AND owner_id = $2`, db.MustUUID(id), db.MustUUID(owner), title)
	if err != nil {
		return Session{}, err
	}
	if tag.RowsAffected() == 0 {
		return Session{}, apperr.ErrNotFound
	}
	return s.GetSession(ctx, owner, id)
}

func (s *Service) DeleteSession(ctx context.Context, owner, id string) error {
	tag, err := s.Pool.Exec(ctx, `DELETE FROM qa_sessions WHERE id = $1 AND owner_id = $2`, db.MustUUID(id), db.MustUUID(owner))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return apperr.ErrNotFound
	}
	return nil
}

func (s *Service) Stop(ctx context.Context, owner, id string) error {
	var exists bool
	err := s.Pool.QueryRow(ctx, `SELECT true FROM qa_sessions WHERE id = $1 AND owner_id = $2`, db.MustUUID(id), db.MustUUID(owner)).Scan(&exists)
	if errors.Is(err, pgx.ErrNoRows) || !exists {
		return apperr.ErrNotFound
	}
	if err != nil {
		return err
	}
	s.Stops.Request(id)
	return nil
}

type AskInput struct {
	Owner     string
	SessionID string
	RequestID string
	Question  string
	Scope     Scope
	WebSearch bool
}

func (s *Service) Ask(ctx context.Context, in AskInput, start func() error, emit func(string, any) error) error {
	s.Stops.Begin(in.SessionID)
	question := strings.TrimSpace(in.Question)
	if question == "" || len([]rune(question)) > 4000 {
		return apperr.WithDetails("VALIDATION_ERROR", "问题不能为空，且不超过 4000 个字符", map[string]any{"field": "question"})
	}
	var title string
	err := s.Pool.QueryRow(ctx, `SELECT title FROM qa_sessions WHERE id = $1 AND owner_id = $2`, db.MustUUID(in.SessionID), db.MustUUID(in.Owner)).Scan(&title)
	if errors.Is(err, pgx.ErrNoRows) {
		return apperr.ErrNotFound
	}
	if err != nil {
		return err
	}
	if _, err := s.Models.Load(ctx, "chat"); err != nil {
		return err
	}
	_ = cryptobox.SHA256Hex([]byte(question))
	docIDs, instruction, threshold, err := s.scope(ctx, in.Owner, in.Scope)
	if err != nil {
		return err
	}
	if len(docIDs) == 0 {
		return apperr.ErrNoReadyDocs
	}
	if err := start(); err != nil {
		return err
	}
	if IsSmalltalk(question) {
		return s.smalltalk(ctx, in, title, question, emit)
	}
	return s.answer(ctx, in, title, question, docIDs, instruction, threshold, emit)
}

func (s *Service) smalltalk(ctx context.Context, in AskInput, title, question string, emit func(string, any) error) error {
	remote, err := s.Models.Load(ctx, "chat")
	if err != nil {
		return s.finishSmall(ctx, in, title, question, "你好，我是研智助手。你可以就已完成的资料向我提问。", true, emit)
	}
	callCtx, cancel := context.WithTimeout(ctx, s.timeout())
	defer cancel()
	var b strings.Builder
	_, err = s.AI.Stream(callCtx, remote, "你是研智助手。用户在寒暄。用一句简短的中文回应，不要引用资料，不要编造出处。", question, func(delta string) error {
		if s.Stops.On(in.SessionID) || ctx.Err() != nil {
			return errStop
		}
		b.WriteString(delta)
		return emit("token", map[string]string{"text": delta})
	})
	if errors.Is(err, errStop) {
		return emit("error", map[string]string{"code": "CANCELLED", "message": "已停止生成", "request_id": in.RequestID})
	}
	if err != nil || strings.TrimSpace(b.String()) == "" {
		return s.finishSmall(ctx, in, title, question, "你好，我是研智助手。你可以就已完成的资料向我提问。", true, emit)
	}
	return s.finishSmall(ctx, in, title, question, strings.TrimSpace(b.String()), true, emit)
}

var errStop = errors.New("stop")

func (s *Service) finishSmall(ctx context.Context, in AskInput, title, question, answer string, send bool, emit func(string, any) error) error {
	if s.Stops.On(in.SessionID) {
		return emit("error", map[string]string{"code": "CANCELLED", "message": "已停止生成", "request_id": in.RequestID})
	}
	if send {
		if err := emit("token", map[string]string{"text": answer}); err != nil {
			return nil
		}
	}
	turnID, err := s.saveTurn(ctx, in, title, question, answer, "smalltalk", false, nil, nil, nil)
	if err != nil {
		return emit("error", map[string]string{"code": "MODEL_UNAVAILABLE", "message": apperr.ErrModelDown.Message, "request_id": in.RequestID})
	}
	return emit("done", map[string]string{"turn_id": turnID, "session_id": in.SessionID})
}

func (s *Service) answer(ctx context.Context, in AskInput, title, question string, docIDs []string, instruction string, threshold float64, emit func(string, any) error) error {
	if s.stopped(in, emit) {
		return nil
	}
	_ = emit("progress", map[string]string{"stage": "search"})
	var vector []float32
	if remote, err := s.Models.Load(ctx, "embedding"); err == nil {
		callCtx, cancel := context.WithTimeout(ctx, s.timeout())
		vecs, _, err := s.AI.Embed(callCtx, remote, []string{question})
		cancel()
		if err == nil && len(vecs) == 1 {
			vector = vecs[0]
		}
	}
	gen, _ := s.Models.ActiveGeneration(ctx)
	ids, err := retrieval.Search(ctx, s.Pool, in.Owner, docIDs, vector, question, gen, threshold, s.K1, s.B, s.RRFK, s.TopK)
	if err != nil {
		return emit("error", map[string]string{"code": "MODEL_UNAVAILABLE", "message": apperr.ErrModelDown.Message, "request_id": in.RequestID})
	}
	if s.stopped(in, emit) {
		return nil
	}
	_ = emit("progress", map[string]string{"stage": "graph"})
	extra, _ := s.Graph.Supplement(ctx, in.Owner, ids, gen, 3)
	sources, err := s.loadSources(ctx, ids, extra)
	if err != nil {
		return emit("error", map[string]string{"code": "MODEL_UNAVAILABLE", "message": apperr.ErrModelDown.Message, "request_id": in.RequestID})
	}
	if s.stopped(in, emit) {
		return nil
	}
	_ = emit("progress", map[string]string{"stage": "generate"})
	items := make([]map[string]any, len(sources))
	aliases := make([]Alias, len(sources))
	var block strings.Builder
	for i, src := range sources {
		items[i] = map[string]any{"alias": src.Alias, "label": Label(src.Title, src.Page), "graph_supplement": src.Graph}
		aliases[i] = Alias{Name: src.Alias, ChunkID: src.ID, DocID: src.DocID, Title: src.Title, Page: src.Page, Graph: src.Graph, Parent: src.Parent, Text: src.Text}
		body := src.Parent
		if strings.TrimSpace(body) == "" {
			body = src.Text
		}
		block.WriteString("[" + src.Alias + "] " + Label(src.Title, src.Page) + "\n<<<DOCUMENT>>>\n" + body + "\n<<<END>>>\n")
	}
	if err := emit("retrieval", map[string]any{"items": items}); err != nil {
		return nil
	}
	history, _ := s.history(ctx, in.SessionID)
	if instruction == "" {
		instruction = "无"
	}
	system := prompts.Render(prompts.QASystem, map[string]string{
		"language": AnswerLanguage(question), "preferences": "无", "kb_instruction": instruction,
		"history": history, "query": question, "sources": block.String(),
	})
	remote, err := s.Models.Load(ctx, "chat")
	if err != nil {
		return emit("error", map[string]string{"code": apperr.ErrModelMissing.Code, "message": apperr.ErrModelMissing.Message, "request_id": in.RequestID})
	}
	dec := NewDecoder(aliases)
	var visible strings.Builder
	sent := false
	callCtx, cancel := context.WithTimeout(ctx, s.timeout())
	defer cancel()
	_, err = s.AI.Stream(callCtx, remote, system, question, func(delta string) error {
		if s.Stops.On(in.SessionID) || ctx.Err() != nil {
			return errStop
		}
		piece := dec.Push(delta)
		if piece == "" {
			return nil
		}
		sent = true
		visible.WriteString(piece)
		return emit("token", map[string]string{"text": piece})
	})
	if errors.Is(err, errStop) || s.Stops.On(in.SessionID) {
		return emit("error", map[string]string{"code": "CANCELLED", "message": "已停止生成", "request_id": in.RequestID})
	}
	if err != nil {
		return emit("error", map[string]string{"code": apperr.ErrModelDown.Code, "message": apperr.ErrModelDown.Message, "request_id": in.RequestID})
	}
	if tail := dec.Flush(); tail != "" {
		visible.WriteString(tail)
		_ = emit("token", map[string]string{"text": tail})
		sent = true
	}
	_ = sent
	answer, used, kind := Finalize(visible.String(), dec.Used(), dec.Refuse)
	if kind == "refused" {
		if err := emit("replace", map[string]string{"text": answer}); err != nil {
			return nil
		}
	}
	citeItems := make([]map[string]any, 0, len(used))
	for _, a := range used {
		citeItems = append(citeItems, map[string]any{
			"chunk_id": a.ChunkID, "document_id": a.DocID, "label": Label(a.Title, a.Page),
			"origin": "kb", "graph_supplement": a.Graph,
		})
	}
	if err := emit("citations", map[string]any{"items": citeItems}); err != nil {
		return nil
	}
	turnID, err := s.saveTurn(ctx, in, title, question, answer, kind, false, ids, extra, used)
	if err != nil {
		return emit("error", map[string]string{"code": apperr.ErrModelDown.Code, "message": apperr.ErrModelDown.Message, "request_id": in.RequestID})
	}
	_ = cryptobox.SHA256Hex([]byte(system))
	return emit("done", map[string]string{"turn_id": turnID, "session_id": in.SessionID})
}

func (s *Service) stopped(in AskInput, emit func(string, any) error) bool {
	if s.Stops.On(in.SessionID) {
		_ = emit("error", map[string]string{"code": "CANCELLED", "message": "已停止生成", "request_id": in.RequestID})
		return true
	}
	return false
}

func (s *Service) scope(ctx context.Context, owner string, scope Scope) ([]string, string, float64, error) {
	switch scope.Type {
	case "", "all", "kb", "tags", "documents":
	default:
		return nil, "", 0, apperr.WithDetails("VALIDATION_ERROR", "检索范围不正确", map[string]any{"field": "scope.type"})
	}
	if _, ok := db.UUIDs(scope.DocumentIDs); !ok {
		return nil, "", 0, apperr.ErrNotFound
	}
	if _, ok := db.UUIDs(scope.TagIDs); !ok {
		return nil, "", 0, apperr.ErrNotFound
	}
	kb := any(nil)
	if scope.Type == "kb" && scope.KBID != "" {
		id, ok := db.UUID(scope.KBID)
		if !ok {
			return nil, "", 0, apperr.ErrNotFound
		}
		kb = id
	}
	docs := uuidArg(scope.DocumentIDs)
	tags := uuidArg(scope.TagIDs)
	if scope.Type != "documents" {
		docs = uuidArg(nil)
	}
	if scope.Type != "tags" {
		tags = uuidArg(nil)
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT d.id::text FROM documents d
		WHERE d.owner_id = $1 AND d.status = 'ready'
		  AND ($2::uuid IS NULL OR d.kb_id = $2)
		  AND (cardinality($3::uuid[]) = 0 OR d.id = ANY($3::uuid[]))
		  AND (cardinality($4::uuid[]) = 0 OR EXISTS (
		    SELECT 1 FROM document_tags t WHERE t.document_id = d.id AND t.tag_id = ANY($4::uuid[])))`,
		db.MustUUID(owner), kb, docs, tags)
	if err != nil {
		return nil, "", 0, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, "", 0, err
		}
		ids = append(ids, id)
	}
	instruction := ""
	threshold := s.Threshold
	if scope.Type == "kb" && scope.KBID != "" {
		var text *string
		var custom *float64
		err := s.Pool.QueryRow(ctx, `
			SELECT answer_instruction, retrieval_threshold::float8 FROM knowledge_bases
			WHERE id = $1 AND owner_id = $2`, db.MustUUID(scope.KBID), db.MustUUID(owner)).Scan(&text, &custom)
		if errors.Is(err, pgx.ErrNoRows) {
			return []string{}, "", threshold, nil
		}
		if err != nil {
			return nil, "", 0, err
		}
		if text != nil {
			instruction = *text
		}
		if custom != nil {
			threshold = *custom
		}
	}
	return ids, instruction, threshold, rows.Err()
}

type srcRow struct {
	ID, DocID, Title, Text, Parent, Alias string
	Page                                   int
	Graph                                  bool
}

func (s *Service) loadSources(ctx context.Context, childIDs, extraIDs []string) ([]srcRow, error) {
	order := append(append([]string{}, childIDs...), extraIDs...)
	if len(order) == 0 {
		return nil, nil
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT c.id::text, c.document_id::text, d.title, c.page_start, c.text, p.text
		FROM chunks c
		JOIN documents d ON d.id = c.document_id
		JOIN parent_chunks p ON p.id = c.parent_id
		WHERE c.id = ANY($1::uuid[])`, uuidArg(order))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	got := map[string]srcRow{}
	for rows.Next() {
		var r srcRow
		if err := rows.Scan(&r.ID, &r.DocID, &r.Title, &r.Page, &r.Text, &r.Parent); err != nil {
			return nil, err
		}
		got[r.ID] = r
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	extraSet := map[string]bool{}
	for _, id := range extraIDs {
		extraSet[id] = true
	}
	var out []srcRow
	ci, gi := 1, 1
	for _, id := range order {
		r, ok := got[id]
		if !ok {
			continue
		}
		if extraSet[id] {
			r.Graph = true
			r.Alias = "g" + itoa(gi)
			gi++
		} else {
			r.Alias = "c" + itoa(ci)
			ci++
		}
		out = append(out, r)
	}
	return out, nil
}

func (s *Service) history(ctx context.Context, sessionID string) (string, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT question, answer FROM qa_turns WHERE session_id = $1
		ORDER BY created_at DESC LIMIT 5`, db.MustUUID(sessionID))
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var lines []string
	for rows.Next() {
		var q, a string
		if err := rows.Scan(&q, &a); err != nil {
			return "", err
		}
		lines = append(lines, "问："+q+"\n答："+a)
	}
	for i, j := 0, len(lines)-1; i < j; i, j = i+1, j-1 {
		lines[i], lines[j] = lines[j], lines[i]
	}
	if len(lines) == 0 {
		return "无", rows.Err()
	}
	return strings.Join(lines, "\n"), rows.Err()
}

func (s *Service) saveTurn(ctx context.Context, in AskInput, title, question, answer, kind string, reranked bool, candidates, extra []string, used []Alias) (string, error) {
	turnID := db.NewID()
	scope, _ := json.Marshal(in.Scope)
	cands := uuidArg(candidates)
	extras := uuidArg(extra)
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, `
		INSERT INTO qa_turns (
			id, session_id, question, answer, answer_kind, rewritten, reranked, web_search,
			scope, candidate_ids, graph_extra_ids, created_at
		) VALUES ($1,$2,$3,$4,$5,false,$6,false,$7,$8,$9, now())`,
		db.MustUUID(turnID), db.MustUUID(in.SessionID), question, answer, kind, reranked, scope, cands, extras)
	if err != nil {
		return "", err
	}
	for _, a := range used {
		_, err = tx.Exec(ctx, `
			INSERT INTO qa_citations (
				id, turn_id, chunk_id, document_id, title_snapshot, page_start, origin, graph_supplement, label
			) VALUES ($1,$2,$3,$4,$5,$6,'kb',$7,$8)`,
			db.MustUUID(db.NewID()), db.MustUUID(turnID), db.MustUUID(a.ChunkID), db.MustUUID(a.DocID),
			a.Title, a.Page, a.Graph, Label(a.Title, a.Page))
		if err != nil {
			return "", err
		}
	}
	next := title
	if title == "新会话" {
		next = question
		if len([]rune(next)) > 40 {
			next = string([]rune(next)[:40])
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE qa_sessions SET title = $2, updated_at = now() WHERE id = $1`, db.MustUUID(in.SessionID), next); err != nil {
		return "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	return turnID, nil
}

func (s *Service) timeout() time.Duration {
	if s.Timeout <= 0 {
		return 60 * time.Second
	}
	return s.Timeout
}

func uuidArg(ids []string) []pgtype.UUID {
	out, ok := db.UUIDs(ids)
	if !ok || out == nil {
		return []pgtype.UUID{}
	}
	return out
}
