package qa

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"yanzhi/server/internal/apperr"
	"yanzhi/server/internal/knowledge"
	"yanzhi/server/internal/platform/db"
	"yanzhi/server/internal/prompts"
	"yanzhi/server/internal/provider"
	"yanzhi/server/internal/retrieval"
)

const (
	defaultStreamTTL    = 15 * time.Minute
	defaultTurnTimeout  = 120 * time.Second
	defaultGraphLimit   = 3
	defaultHistoryLimit = 5
	persistTimeout      = 5 * time.Second
)

// Option 配置 qa.Service 的可选运行参数与扩展槽位。
type Option func(*Service)

func WithStreamTTL(d time.Duration) Option {
	return func(s *Service) {
		if d > 0 {
			s.streamTTL = d
		}
	}
}

func WithTurnTimeout(d time.Duration) Option {
	return func(s *Service) {
		if d > 0 {
			s.turnTimeout = d
		}
	}
}

func WithGraphLimit(n int) Option {
	return func(s *Service) {
		if n > 0 {
			s.graphLimit = n
		}
	}
}

func WithHistoryLimit(n int) Option {
	return func(s *Service) {
		if n > 0 {
			s.historyLimit = n
		}
	}
}

func WithNowFunc(fn func() time.Time) Option {
	return func(s *Service) {
		if fn != nil {
			s.nowFn = fn
		}
	}
}

func WithPreferences(r PreferenceReader) Option {
	return func(s *Service) { s.prefs = r }
}

func WithFAQMatcher(m FAQMatcher) Option {
	return func(s *Service) { s.faq = m }
}

func WithWebSearcher(w WebSearcher) Option {
	return func(s *Service) { s.web = w }
}

func WithFeedbackRecorder(f FeedbackRecorder) Option {
	return func(s *Service) { s.feedback = f }
}

// Service 协调会话管理、检索范围闸门、多轮查询改写、混合检索、Scoped 图谱补充与流式回答落库。
type Service struct {
	appCtx       context.Context
	store        Store
	scope        knowledge.ScopeResolver
	models       provider.Gateway
	ret          Retriever
	graph        GraphPort
	broker       *StreamBroker
	streamTTL    time.Duration
	turnTimeout  time.Duration
	graphLimit   int
	historyLimit int
	nowFn        func() time.Time

	// Slice 2/3 可选扩展钩子（默认 nil，不触发未建表依赖）
	prefs    PreferenceReader
	faq      FAQMatcher
	web      WebSearcher
	feedback FeedbackRecorder
}

func NewService(
	appCtx context.Context,
	store Store,
	scope knowledge.ScopeResolver,
	models provider.Gateway,
	ret Retriever,
	gp GraphPort,
	opts ...Option,
) *Service {
	if appCtx == nil {
		appCtx = context.Background()
	}
	s := &Service{
		appCtx:       appCtx,
		store:        store,
		scope:        scope,
		models:       models,
		ret:          ret,
		graph:        gp,
		streamTTL:    defaultStreamTTL,
		turnTimeout:  defaultTurnTimeout,
		graphLimit:   defaultGraphLimit,
		historyLimit: defaultHistoryLimit,
		nowFn:        func() time.Time { return time.Now().UTC() },
	}
	for _, opt := range opts {
		if opt != nil {
			opt(s)
		}
	}
	s.broker = NewStreamBroker(s.streamTTL)
	s.broker.startJanitor(s.appCtx, s.nowFn)
	return s
}

func (s *Service) CreateSession(ctx context.Context, owner, title string) (Session, error) {
	return s.store.CreateSession(ctx, owner, title)
}

func (s *Service) ListSessions(ctx context.Context, owner string, page, size int) ([]Session, int, error) {
	return s.store.ListSessions(ctx, owner, page, size)
}

func (s *Service) GetSession(ctx context.Context, owner, id string) (Session, error) {
	return s.store.GetSession(ctx, owner, id)
}

func (s *Service) PatchSession(ctx context.Context, owner, id, title string) (Session, error) {
	return s.store.PatchSession(ctx, owner, id, title)
}

func (s *Service) DeleteSession(ctx context.Context, owner, id string) error {
	return s.store.DeleteSession(ctx, owner, id)
}

// SweepExpiredStreams 供运维清理或单测确定性触发 15 分钟重放窗口淘汰。
func (s *Service) SweepExpiredStreams(now time.Time) int {
	return s.broker.SweepExpired(now)
}

type turnRunInput struct {
	reqID        string
	owner        string
	sessionID    string
	turnID       string
	currentTitle string
	question     string
	askInput     AskInput
	resolved     knowledge.ResolvedScope
}

type retrievalWireItem struct {
	Alias           string `json:"alias"`
	ChunkID         string `json:"chunk_id"`
	DocumentID      string `json:"document_id"`
	Title           string `json:"title"`
	Page            int    `json:"page"`
	Label           string `json:"label"`
	GraphSupplement bool   `json:"graph_supplement"`
}

// StartTurn 同步完成提问校验、会话归属、作用域就绪与聊天模型配置闸门；
// 通过后立即脱离 HTTP 请求上下文在 appCtx 下异步生成，并返回事件游标订阅。
func (s *Service) StartTurn(ctx context.Context, owner, sessionID, reqID string, in AskInput) (TurnHandle, error) {
	question := strings.TrimSpace(in.Question)
	if rs := []rune(question); len(rs) == 0 || len(rs) > 4000 {
		return TurnHandle{}, apperr.WithDetails("VALIDATION_ERROR", "问题需要 1 到 4000 个字符", map[string]any{"field": "question"})
	}
	currentTitle, err := s.store.EnsureSessionOwner(ctx, owner, sessionID)
	if err != nil {
		return TurnHandle{}, err
	}
	resolved, err := s.scope.ResolveScope(ctx, owner, in.Scope)
	if err != nil {
		if errors.Is(err, apperr.ErrNoReadyDocs) && IsSmalltalk(question) {
			resolved = knowledge.EmptyScope(owner, in.Scope)
		} else {
			return TurnHandle{}, err
		}
	}
	if s.models == nil {
		return TurnHandle{}, apperr.ErrModelMissing
	}
	if _, err := s.models.RequireModel(ctx, "chat"); err != nil {
		return TurnHandle{}, err
	}

	turnID := db.NewID()
	ts, genCtx := s.broker.Register(s.appCtx, owner, sessionID, turnID, s.turnTimeout)
	sub := ts.Subscribe(0)

	go s.runTurn(genCtx, ts, turnRunInput{
		reqID:        reqID,
		owner:        owner,
		sessionID:    sessionID,
		turnID:       turnID,
		currentTitle: currentTitle,
		question:     question,
		askInput:     in,
		resolved:     resolved,
	})

	return TurnHandle{
		TurnID: turnID,
		Stream: sub,
	}, nil
}

// StopSession 校验会话归属后立即取消该会话上所有活跃轮次的生成协程。
func (s *Service) StopSession(ctx context.Context, owner, sessionID string) error {
	if _, err := s.store.EnsureSessionOwner(ctx, owner, sessionID); err != nil {
		return err
	}
	s.broker.StopSession(owner, sessionID)
	return nil
}

// ResumeTurn 在 15 分钟内存重放窗口内按 afterSeq 续传或重放 SSE 事件；
// 若已过窗但数据库已有该轮记录返回 ErrStreamClosed (409)，均不存在则返回 ErrNotFound (404)。
func (s *Service) ResumeTurn(ctx context.Context, owner, sessionID, turnID string, afterSeq int64) (*StreamSubscription, error) {
	if _, err := s.store.EnsureSessionOwner(ctx, owner, sessionID); err != nil {
		return nil, err
	}
	if ts, ok := s.broker.Lookup(owner, sessionID, turnID); ok {
		return ts.Subscribe(afterSeq), nil
	}
	exists, err := s.store.TurnExists(ctx, owner, sessionID, turnID)
	if err != nil {
		return nil, err
	}
	if exists {
		return nil, apperr.ErrStreamClosed
	}
	return nil, apperr.ErrNotFound
}

func (s *Service) runTurn(genCtx context.Context, ts *TurnStream, in turnRunInput) {
	defer s.broker.MarkFinished(ts, s.nowFn())

	if IsSmalltalk(in.question) {
		s.runSmalltalkTurn(genCtx, ts, in)
		return
	}

	// 1. 读取最近 5 轮持久化历史
	history, err := s.store.RecentHistory(genCtx, in.owner, in.sessionID, s.historyLimit)
	if err != nil {
		s.publishFailure(ts, genCtx, in.reqID, err, "INTERNAL_ERROR", "加载历史记录失败")
		return
	}
	if s.isCancelled(ts, genCtx) {
		s.publishCancelled(ts, in.reqID)
		return
	}

	// 2. FR-10 查询改写：存在多轮历史时触发；失败或空结果静默降级为原问题
	searchQuery := in.question
	rewritten := false
	historyText := formatHistory(history)
	if len(history) > 0 {
		ts.Publish("progress", map[string]any{"stage": "rewrite"})
		if q, ok := s.rewriteQuery(genCtx, ts, in, historyText); ok {
			searchQuery = q
			rewritten = true
		}
		if s.isCancelled(ts, genCtx) {
			s.publishCancelled(ts, in.reqID)
			return
		}
	}

	// 3. FR-11 混合检索与重排（含阈值过滤与 Top-5 截断）
	ts.Publish("progress", map[string]any{"stage": "search"})
	searchRes, err := s.ret.Search(genCtx, in.reqID, in.resolved, searchQuery)
	if s.isCancelled(ts, genCtx) {
		s.publishCancelled(ts, in.reqID)
		return
	}
	if err != nil {
		s.publishFailure(ts, genCtx, in.reqID, err, "MODEL_UNAVAILABLE", "检索失败")
		return
	}
	if len(searchRes.Hits) == 0 {
		s.finishRefusedTurn(ts, genCtx, in, rewritten, searchRes.Reranked, nil, nil)
		return
	}

	// 4. FR-07 受 scope.GraphDocIDs() 严格约束的一跳图谱补充，不挤占向量/BM25 Top-5 槽位
	candIDs := make([]string, 0, len(searchRes.Hits))
	seenChunk := make(map[string]struct{}, len(searchRes.Hits))
	for _, h := range searchRes.Hits {
		candIDs = append(candIDs, h.ChunkID)
		seenChunk[h.ChunkID] = struct{}{}
	}
	var graphExtraIDs []string
	var graphHits []retrieval.Hit
	if s.graph != nil && len(in.resolved.GraphDocIDs()) > 0 {
		ts.Publish("progress", map[string]any{"stage": "graph"})
		extraIDs, gErr := s.graph.Supplement(genCtx, in.resolved, candIDs, searchRes.Generation, s.graphLimit)
		if s.isCancelled(ts, genCtx) {
			s.publishCancelled(ts, in.reqID)
			return
		}
		if gErr == nil && len(extraIDs) > 0 {
			for _, id := range extraIDs {
				if _, dup := seenChunk[id]; !dup {
					seenChunk[id] = struct{}{}
					graphExtraIDs = append(graphExtraIDs, id)
				}
			}
			if len(graphExtraIDs) > 0 {
				hydrated, hErr := s.ret.Hydrate(genCtx, in.resolved, graphExtraIDs)
				if s.isCancelled(ts, genCtx) {
					s.publishCancelled(ts, in.reqID)
					return
				}
				if hErr == nil {
					graphHits = hydrated
				}
			}
		}
	}

	// 5. 装配引用别名并推送 retrieval 事件
	aliases := make([]Alias, 0, len(searchRes.Hits)+len(graphHits))
	wireRetrieval := make([]retrievalWireItem, 0, len(searchRes.Hits)+len(graphHits))
	for i, h := range searchRes.Hits {
		name := fmt.Sprintf("c%d", i+1)
		aliases = append(aliases, Alias{
			Name:    name,
			ChunkID: h.ChunkID,
			DocID:   h.DocumentID,
			Title:   h.Title,
			Page:    h.PageStart,
			Graph:   false,
			Parent:  h.ParentText,
			Text:    h.Text,
		})
		wireRetrieval = append(wireRetrieval, retrievalWireItem{
			Alias:           name,
			ChunkID:         h.ChunkID,
			DocumentID:      h.DocumentID,
			Title:           h.Title,
			Page:            h.PageStart,
			Label:           Label(h.Title, h.PageStart),
			GraphSupplement: false,
		})
	}
	for i, h := range graphHits {
		name := fmt.Sprintf("g%d", i+1)
		aliases = append(aliases, Alias{
			Name:    name,
			ChunkID: h.ChunkID,
			DocID:   h.DocumentID,
			Title:   h.Title,
			Page:    h.PageStart,
			Graph:   true,
			Parent:  h.ParentText,
			Text:    h.Text,
		})
		wireRetrieval = append(wireRetrieval, retrievalWireItem{
			Alias:           name,
			ChunkID:         h.ChunkID,
			DocumentID:      h.DocumentID,
			Title:           h.Title,
			Page:            h.PageStart,
			Label:           Label(h.Title, h.PageStart),
			GraphSupplement: true,
		})
	}
	ts.Publish("retrieval", map[string]any{"items": wireRetrieval})

	// 6. 渲染系统提示词并流式生成回答
	prefsText := "无"
	if s.prefs != nil {
		if p, pErr := s.prefs.LoadConfirmed(genCtx, in.owner); pErr == nil && strings.TrimSpace(p) != "" {
			prefsText = strings.TrimSpace(p)
		}
		if s.isCancelled(ts, genCtx) {
			s.publishCancelled(ts, in.reqID)
			return
		}
	}
	kbInstr := strings.TrimSpace(in.resolved.AnswerInstruction())
	if kbInstr == "" {
		kbInstr = "无"
	}
	systemPrompt := prompts.Render(prompts.QASystem, map[string]string{
		"language":       AnswerLanguage(in.question),
		"preferences":    prefsText,
		"kb_instruction": kbInstr,
		"history":        historyText,
		"query":          in.question,
		"sources":        formatSources(aliases),
	})

	ts.Publish("progress", map[string]any{"stage": "generate"})
	dec := NewDecoder(aliases)
	var visibleBuf strings.Builder
	streamErr := s.models.Stream(genCtx, provider.CallMeta{
		RequestID:  in.reqID,
		UserID:     in.owner,
		Kind:       "chat",
		PromptHash: provider.PromptSHA256(systemPrompt, in.question),
	}, systemPrompt, in.question, func(delta string) error {
		if ts.StopRequested() {
			return context.Canceled
		}
		visible := dec.Push(delta)
		if visible != "" {
			visibleBuf.WriteString(visible)
			ts.Publish("token", map[string]any{"text": visible})
		}
		return nil
	})
	if s.isCancelled(ts, genCtx) || errors.Is(streamErr, context.Canceled) {
		s.publishCancelled(ts, in.reqID)
		return
	}
	if streamErr != nil {
		s.publishFailure(ts, genCtx, in.reqID, streamErr, "MODEL_UNAVAILABLE", "生成服务暂时不可用")
		return
	}
	if tail := dec.Flush(); tail != "" {
		if s.isCancelled(ts, genCtx) {
			s.publishCancelled(ts, in.reqID)
			return
		}
		visibleBuf.WriteString(tail)
		ts.Publish("token", map[string]any{"text": tail})
	}

	finalAnswer, usedAliases, answerKind := Finalize(visibleBuf.String(), dec.Used(), dec.Refuse)
	if answerKind == "refused" {
		s.finishRefusedTurn(ts, genCtx, in, rewritten, searchRes.Reranked, candIDs, graphExtraIDs)
		return
	}

	persistCites, wireCites := buildCitations(usedAliases)
	ts.Publish("citations", map[string]any{"items": wireCites})

	if ts.StopRequested() {
		s.publishCancelled(ts, in.reqID)
		return
	}
	if err := s.persistTurn(in, PersistTurn{
		ID:            in.turnID,
		SessionID:     in.sessionID,
		Question:      in.question,
		Answer:        finalAnswer,
		AnswerKind:    answerKind,
		Rewritten:     rewritten,
		Reranked:      searchRes.Reranked,
		WebSearch:     in.askInput.WebSearch,
		Scope:         in.resolved.WireScope(),
		CandidateIDs:  candIDs,
		GraphExtraIDs: graphExtraIDs,
		Citations:     persistCites,
		CreatedAt:     s.nowFn().UTC(),
	}); err != nil {
		ts.Publish("error", map[string]any{"code": "INTERNAL_ERROR", "message": "保存回答失败", "request_id": in.reqID})
		return
	}

	ts.Publish("done", map[string]any{
		"session_id":  in.sessionID,
		"answer_kind": answerKind,
		"rewritten":   rewritten,
		"reranked":    searchRes.Reranked,
	})
}

func (s *Service) runSmalltalkTurn(genCtx context.Context, ts *TurnStream, in turnRunInput) {
	systemPrompt := "你是研智助手。请用" + AnswerLanguage(in.question) + "简短友好地回应用户的问候或礼貌用语。"
	var answerBuf strings.Builder
	streamErr := s.models.Stream(genCtx, provider.CallMeta{
		RequestID:  in.reqID,
		UserID:     in.owner,
		Kind:       "chat",
		PromptHash: provider.PromptSHA256(systemPrompt, in.question),
	}, systemPrompt, in.question, func(delta string) error {
		if ts.StopRequested() {
			return context.Canceled
		}
		if delta != "" {
			answerBuf.WriteString(delta)
			ts.Publish("token", map[string]any{"text": delta})
		}
		return nil
	})
	if s.isCancelled(ts, genCtx) || errors.Is(streamErr, context.Canceled) {
		s.publishCancelled(ts, in.reqID)
		return
	}
	if streamErr != nil {
		s.publishFailure(ts, genCtx, in.reqID, streamErr, "MODEL_UNAVAILABLE", "生成服务暂时不可用")
		return
	}

	if ts.StopRequested() {
		s.publishCancelled(ts, in.reqID)
		return
	}
	if err := s.persistTurn(in, PersistTurn{
		ID:         in.turnID,
		SessionID:  in.sessionID,
		Question:   in.question,
		Answer:     strings.TrimSpace(answerBuf.String()),
		AnswerKind: "smalltalk",
		Rewritten:  false,
		Reranked:   false,
		WebSearch:  in.askInput.WebSearch,
		Scope:      in.resolved.WireScope(),
		CreatedAt:  s.nowFn().UTC(),
	}); err != nil {
		ts.Publish("error", map[string]any{"code": "INTERNAL_ERROR", "message": "保存回答失败", "request_id": in.reqID})
		return
	}
	ts.Publish("done", map[string]any{
		"session_id":  in.sessionID,
		"answer_kind": "smalltalk",
		"rewritten":   false,
		"reranked":    false,
	})
}

func (s *Service) rewriteQuery(genCtx context.Context, ts *TurnStream, in turnRunInput, historyText string) (string, bool) {
	rendered := prompts.Render(prompts.QARewrite, map[string]string{
		"history": historyText,
		"query":   in.question,
	})
	out, err := s.models.Complete(genCtx, provider.CallMeta{
		RequestID:  in.reqID,
		UserID:     in.owner,
		Kind:       "chat",
		PromptHash: provider.PromptSHA256(rendered),
	}, "", rendered)
	if err != nil || ts.StopRequested() || genCtx.Err() != nil {
		return "", false
	}
	cleaned := strings.Join(strings.Fields(strings.TrimSpace(out)), " ")
	if cleaned == "" || cleaned == in.question {
		return "", false
	}
	return cleaned, true
}

func (s *Service) finishRefusedTurn(ts *TurnStream, genCtx context.Context, in turnRunInput, rewritten, reranked bool, candIDs, graphExtraIDs []string) {
	if s.isCancelled(ts, genCtx) {
		s.publishCancelled(ts, in.reqID)
		return
	}
	ts.Publish("replace", map[string]any{"text": Refusal})
	ts.Publish("citations", map[string]any{"items": []Citation{}})
	if ts.StopRequested() {
		s.publishCancelled(ts, in.reqID)
		return
	}
	if err := s.persistTurn(in, PersistTurn{
		ID:            in.turnID,
		SessionID:     in.sessionID,
		Question:      in.question,
		Answer:        Refusal,
		AnswerKind:    "refused",
		Rewritten:     rewritten,
		Reranked:      reranked,
		WebSearch:     in.askInput.WebSearch,
		Scope:         in.resolved.WireScope(),
		CandidateIDs:  slices.Clone(candIDs),
		GraphExtraIDs: slices.Clone(graphExtraIDs),
		CreatedAt:     s.nowFn().UTC(),
	}); err != nil {
		ts.Publish("error", map[string]any{"code": "INTERNAL_ERROR", "message": "保存回答失败", "request_id": in.reqID})
		return
	}
	ts.Publish("done", map[string]any{
		"session_id":  in.sessionID,
		"answer_kind": "refused",
		"rewritten":   rewritten,
		"reranked":    reranked,
	})
}

func (s *Service) persistTurn(in turnRunInput, turn PersistTurn) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(s.appCtx), persistTimeout)
	defer cancel()
	return s.store.SaveTurn(ctx, in.currentTitle, turn)
}

func (s *Service) isCancelled(ts *TurnStream, genCtx context.Context) bool {
	return ts.StopRequested() || errors.Is(genCtx.Err(), context.Canceled)
}

func (s *Service) publishCancelled(ts *TurnStream, reqID string) {
	ts.Publish("error", map[string]any{
		"code":       "CANCELLED",
		"message":    "已停止生成",
		"request_id": reqID,
	})
}

func (s *Service) publishFailure(ts *TurnStream, genCtx context.Context, reqID string, err error, fallbackCode, fallbackMsg string) {
	if s.isCancelled(ts, genCtx) || errors.Is(err, context.Canceled) {
		s.publishCancelled(ts, reqID)
		return
	}
	code := fallbackCode
	msg := fallbackMsg
	if errors.Is(genCtx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		code = "MODEL_UNAVAILABLE"
		msg = "生成超时"
	} else if ae := apperr.As(err); ae != nil {
		code = ae.Code
		msg = ae.Message
	}
	ts.Publish("error", map[string]any{
		"code":       code,
		"message":    msg,
		"request_id": reqID,
	})
}

func buildCitations(used []Alias) ([]PersistCitation, []Citation) {
	persist := make([]PersistCitation, 0, len(used))
	wire := make([]Citation, 0, len(used))
	seen := make(map[string]struct{}, len(used))
	for _, a := range used {
		if _, dup := seen[a.ChunkID]; dup {
			continue
		}
		seen[a.ChunkID] = struct{}{}
		lbl := Label(a.Title, a.Page)
		persist = append(persist, PersistCitation{
			ChunkID:         a.ChunkID,
			DocumentID:      a.DocID,
			TitleSnapshot:   a.Title,
			PageStart:       a.Page,
			Origin:          "kb",
			GraphSupplement: a.Graph,
			Label:           lbl,
		})
		cid := a.ChunkID
		did := a.DocID
		wire = append(wire, Citation{
			ChunkID:         &cid,
			DocumentID:      &did,
			Label:           lbl,
			Origin:          "kb",
			GraphSupplement: a.Graph,
		})
	}
	return persist, wire
}

func formatHistory(turns []HistoryTurn) string {
	if len(turns) == 0 {
		return "无"
	}
	var b strings.Builder
	for i, t := range turns {
		if i > 0 {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "%d. 用户：%s\n   助手：%s", i+1, t.Question, t.Answer)
	}
	return b.String()
}

func formatSources(aliases []Alias) string {
	var b strings.Builder
	for i, a := range aliases {
		if i > 0 {
			b.WriteByte('\n')
		}
		lbl := Label(a.Title, a.Page)
		if a.Graph {
			fmt.Fprintf(&b, "[%s] %s（关联补充）\n<<<DOCUMENT>>>\n", a.Name, lbl)
		} else {
			fmt.Fprintf(&b, "[%s] %s\n<<<DOCUMENT>>>\n", a.Name, lbl)
		}
		parent := strings.TrimSpace(a.Parent)
		text := strings.TrimSpace(a.Text)
		if parent != "" && parent != text {
			b.WriteString("所属父段落：\n")
			b.WriteString(parent)
			b.WriteString("\n命中片段：\n")
			b.WriteString(text)
		} else {
			b.WriteString(text)
		}
		b.WriteString("\n<<<END>>>")
	}
	return b.String()
}
