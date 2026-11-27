package qa_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"yanzhi/server/internal/apperr"
	"yanzhi/server/internal/graph"
	"yanzhi/server/internal/knowledge"
	"yanzhi/server/internal/provider"
	"yanzhi/server/internal/qa"
	"yanzhi/server/internal/retrieval"
)

type qaHarness struct {
	ctx      context.Context
	cancel   context.CancelFunc
	kb       *knowledge.MemoryStore
	qaStore  *qa.MemoryStore
	gw       *provider.MemoryGateway
	graphMem *graph.MemoryStore
	ret      *retrieval.Engine
	svc      *qa.Service

	mu  sync.Mutex
	now time.Time
}

func newHarness(t *testing.T, opts ...qa.Option) *qaHarness {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	kb := knowledge.NewMemoryStore(nil, nil, nil)
	qaStore := qa.NewMemoryStore(qa.WithAliveCheck(qa.MemoryAliveChecker(kb)))
	gw := provider.NewMemoryGateway()
	graphMem := graph.NewMemoryStore()
	ret := retrieval.NewEngine(
		retrieval.NewMemoryCorpus(qa.MemoryChunkSource(kb)),
		gw,
		retrieval.DefaultSearchConfig(),
	)

	h := &qaHarness{
		ctx:      ctx,
		cancel:   cancel,
		kb:       kb,
		qaStore:  qaStore,
		gw:       gw,
		graphMem: graphMem,
		ret:      ret,
		now:      time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC),
	}
	baseOpts := []qa.Option{
		qa.WithNowFunc(func() time.Time {
			h.mu.Lock()
			defer h.mu.Unlock()
			return h.now
		}),
	}
	allOpts := append(baseOpts, opts...)
	h.svc = qa.NewService(ctx, qaStore, kb, gw, ret, graphMem, allOpts...)
	return h
}

func (h *qaHarness) advance(d time.Duration) time.Time {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.now = h.now.Add(d)
	return h.now
}

func samplePDF() []byte {
	return []byte("%PDF-1.4\n/Type /Page\n%%EOF")
}

func seedReadyDoc(t *testing.T, kb *knowledge.MemoryStore, owner, kbID, title string, gen int64, parents []knowledge.MemoryParentChunk, chunks []knowledge.MemoryChunkInput) string {
	t.Helper()
	res, err := kb.Upload(context.Background(), owner, kbID, knowledge.UploadMeta{
		FileName: title + ".pdf",
		Title:    title,
	}, []byte(fmt.Sprintf("%%PDF-1.4\n%% %s-%s\n/Type /Page\n%%%%EOF", title, uuid.NewString())))
	if err != nil {
		t.Fatalf("upload doc %s: %v", title, err)
	}
	if err := kb.SeedChunks(owner, res.Doc.ID, gen, parents, chunks); err != nil {
		t.Fatalf("seed chunks %s: %v", title, err)
	}
	if err := kb.SetDocStatus(owner, res.Doc.ID, "ready"); err != nil {
		t.Fatalf("set doc ready %s: %v", title, err)
	}
	return res.Doc.ID
}

func collectEvents(ctx context.Context, sub *qa.StreamSubscription) []qa.StreamEvent {
	var evts []qa.StreamEvent
	for {
		evt, ok := sub.Next(ctx)
		if !ok {
			return evts
		}
		evts = append(evts, evt)
	}
}

func decodeEventMap(t *testing.T, evt qa.StreamEvent) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(evt.Data, &m); err != nil {
		t.Fatalf("unmarshal event %s: %v", evt.Name, err)
	}
	return m
}

func TestStartTurnSmalltalkBypassesNoReadyDocsAndSkipsRetrieval(t *testing.T) {
	h := newHarness(t)
	owner := uuid.NewString()
	sess, err := h.svc.CreateSession(h.ctx, owner, "")
	if err != nil {
		t.Fatal(err)
	}
	if sess.Title != "新会话" {
		t.Fatalf("expected default title 新会话, got %q", sess.Title)
	}

	h.gw.SetModel("chat", provider.Remote{Kind: "chat", Model: "gpt-test"})
	h.gw.StreamFn = func(ctx context.Context, system, user string, onDelta func(string) error) error {
		if err := onDelta("你好！"); err != nil {
			return err
		}
		return onDelta("很高兴为你服务。")
	}

	// 用户 0 就绪文档时发送寒暄仍应允许通过且不调用向量检索或图谱。
	handle, err := h.svc.StartTurn(h.ctx, owner, sess.ID, "req-st-1", qa.AskInput{
		Question: "你好！",
		Scope:    knowledge.ScopeInput{Type: "all"},
	})
	if err != nil {
		t.Fatalf("expected smalltalk bypass, got err: %v", err)
	}

	readCtx, cancel := context.WithTimeout(h.ctx, 3*time.Second)
	defer cancel()
	evts := collectEvents(readCtx, handle.Stream)
	if len(evts) == 0 {
		t.Fatal("expected SSE events")
	}

	var tokens strings.Builder
	var sawDone bool
	for i, e := range evts {
		if e.Seq != int64(i+1) {
			t.Fatalf("expected monotonic seq %d, got %d", i+1, e.Seq)
		}
		switch e.Name {
		case "progress", "retrieval", "citations":
			t.Fatalf("smalltalk must only emit token and done, got forbidden event %q (%s)", e.Name, e.Data)
		case "token":
			tokens.WriteString(decodeEventMap(t, e)["text"].(string))
		case "done":
			sawDone = true
			m := decodeEventMap(t, e)
			if m["answer_kind"] != "smalltalk" {
				t.Fatalf("expected smalltalk answer_kind, got %v", m["answer_kind"])
			}
			if m["session_id"] != sess.ID {
				t.Fatalf("expected done session_id=%q, got %v", sess.ID, m["session_id"])
			}
		}
	}
	if !sawDone || tokens.String() != "你好！很高兴为你服务。" {
		t.Fatalf("unexpected smalltalk stream: done=%v tokens=%q", sawDone, tokens.String())
	}

	for _, c := range h.gw.Calls() {
		if c.Kind == "embedding" || c.Kind == "rerank" {
			t.Fatalf("smalltalk must not invoke %s", c.Kind)
		}
	}

	detail, err := h.svc.GetSession(h.ctx, owner, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Title != "你好！" {
		t.Fatalf("expected auto session title updated to 你好！, got %q", detail.Title)
	}
	if len(detail.Turns) != 1 || detail.Turns[0].AnswerKind != "smalltalk" {
		t.Fatalf("expected 1 persisted smalltalk turn, got %+v", detail.Turns)
	}
}

func TestStartTurnPreflightErrorsPrecedence(t *testing.T) {
	h := newHarness(t)
	owner := uuid.NewString()
	otherOwner := uuid.NewString()

	sess, err := h.svc.CreateSession(h.ctx, owner, "测试")
	if err != nil {
		t.Fatal(err)
	}
	kb, err := h.kb.CreateKB(h.ctx, owner, "空知识库")
	if err != nil {
		t.Fatal(err)
	}
	otherKB, err := h.kb.CreateKB(h.ctx, otherOwner, "他人知识库")
	if err != nil {
		t.Fatal(err)
	}

	// 1. 问题为空或超长 -> 422
	_, err = h.svc.StartTurn(h.ctx, owner, sess.ID, "r1", qa.AskInput{Question: "   "})
	if ae := apperr.As(err); ae == nil || ae.Code != "VALIDATION_ERROR" {
		t.Fatalf("expected VALIDATION_ERROR for blank question, got %v", err)
	}
	_, err = h.svc.StartTurn(h.ctx, owner, sess.ID, "r2", qa.AskInput{Question: strings.Repeat("问", 4001)})
	if ae := apperr.As(err); ae == nil || ae.Code != "VALIDATION_ERROR" {
		t.Fatalf("expected VALIDATION_ERROR for long question, got %v", err)
	}

	// 2. 非法或他人会话 -> 404
	_, err = h.svc.StartTurn(h.ctx, otherOwner, sess.ID, "r3", qa.AskInput{Question: "hello"})
	if !errors.Is(err, apperr.ErrNotFound) {
		t.Fatalf("expected 404 for foreign session, got %v", err)
	}

	// 3. 即使问题为寒暄，目标 KB 归属他人或格式非法也必须先被 404 / 422 拦截，绝不能被 smalltalk 放行
	_, err = h.svc.StartTurn(h.ctx, owner, sess.ID, "r4", qa.AskInput{
		Question: "你好",
		Scope:    knowledge.ScopeInput{Type: "kb", KBID: otherKB.ID},
	})
	if !errors.Is(err, apperr.ErrNotFound) {
		t.Fatalf("expected 404 for foreign kb even on smalltalk, got %v", err)
	}
	_, err = h.svc.StartTurn(h.ctx, owner, sess.ID, "r5", qa.AskInput{
		Question: "你好",
		Scope:    knowledge.ScopeInput{Type: "invalid_type"},
	})
	if ae := apperr.As(err); ae == nil || ae.Code != "VALIDATION_ERROR" {
		t.Fatalf("expected 422 for invalid scope even on smalltalk, got %v", err)
	}

	// 4. 非寒暄遇到空/未就绪文档库 -> 409 NO_READY_DOCUMENTS（甚至先于 MODEL_NOT_CONFIGURED）
	h.gw.RemoveModel("chat")
	_, err = h.svc.StartTurn(h.ctx, owner, sess.ID, "r6", qa.AskInput{
		Question: "什么是 Attention？",
		Scope:    knowledge.ScopeInput{Type: "kb", KBID: kb.ID},
	})
	if !errors.Is(err, apperr.ErrNoReadyDocs) {
		t.Fatalf("expected NO_READY_DOCUMENTS, got %v", err)
	}

	// 5. 注入就绪文档但未配置 chat 模型 -> 409 MODEL_NOT_CONFIGURED
	seedReadyDoc(t, h.kb, owner, kb.ID, "Paper One", 1,
		[]knowledge.MemoryParentChunk{{ID: uuid.NewString(), PageStart: 1, Text: "parent"}},
		[]knowledge.MemoryChunkInput{{ID: uuid.NewString(), PageStart: 1, Text: "attention mechanism"}},
	)
	_, err = h.svc.StartTurn(h.ctx, owner, sess.ID, "r7", qa.AskInput{
		Question: "什么是 Attention？",
		Scope:    knowledge.ScopeInput{Type: "kb", KBID: kb.ID},
	})
	if !errors.Is(err, apperr.ErrModelMissing) {
		t.Fatalf("expected MODEL_NOT_CONFIGURED, got %v", err)
	}
}

func TestMultiTurnRewriteAndFallback(t *testing.T) {
	h := newHarness(t)
	owner := uuid.NewString()
	sess, err := h.svc.CreateSession(h.ctx, owner, "多轮会话")
	if err != nil {
		t.Fatal(err)
	}
	kb, err := h.kb.CreateKB(h.ctx, owner, "ML")
	if err != nil {
		t.Fatal(err)
	}
	pID := uuid.NewString()
	docID := seedReadyDoc(t, h.kb, owner, kb.ID, "Transformer Paper", 1,
		[]knowledge.MemoryParentChunk{{ID: pID, PageStart: 2, Text: "Transformer architecture avoids recurrence and enables parallel training."}},
		[]knowledge.MemoryChunkInput{{ID: uuid.NewString(), ParentID: pID, PageStart: 2, Text: "Transformer enables parallel training and models long-range attention.", Embedding: []float32{1, 0}}},
	)
	_ = docID

	h.gw.SetModel("chat", provider.Remote{Kind: "chat", Model: "chat-v1"})
	h.gw.SetModel("embedding", provider.Remote{Kind: "embedding", Model: "emb-v1"})
	h.gw.EmbedFn = func(ctx context.Context, inputs []string) ([][]float32, error) {
		return [][]float32{{1, 0}}, nil
	}

	// 第一轮无历史：不触发 rewrite
	var rewriteCalled bool
	h.gw.CompleteFn = func(ctx context.Context, system, user string) (string, error) {
		rewriteCalled = true
		return "should not be called on turn 1", nil
	}
	h.gw.StreamFn = func(ctx context.Context, system, user string, onDelta func(string) error) error {
		return onDelta("Transformer 可以并行训练 [[c1]]。")
	}

	h1, err := h.svc.StartTurn(h.ctx, owner, sess.ID, "req-1", qa.AskInput{
		Question: "Transformer 有什么特点？",
		Scope:    knowledge.ScopeInput{Type: "kb", KBID: kb.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	evts1 := collectEvents(h.ctx, h1.Stream)
	if rewriteCalled {
		t.Fatal("turn 1 without history must not call rewrite")
	}
	if len(evts1) == 0 || decodeEventMap(t, evts1[len(evts1)-1])["rewritten"] != false {
		t.Fatalf("expected turn 1 done rewritten=false, got %+v", evts1)
	}

	// 第二轮有历史：触发 FR-10 改写，且下游使用改写后包含关键字的查询
	var sawRewritePrompt string
	var seenSystem string
	h.gw.CompleteFn = func(ctx context.Context, system, user string) (string, error) {
		sawRewritePrompt = user
		return "Transformer parallel training attention", nil
	}
	h.gw.StreamFn = func(ctx context.Context, system, user string, onDelta func(string) error) error {
		seenSystem = system
		return onDelta("它通过并行训练建模长距离注意力 [[c1]]。")
	}
	h2, err := h.svc.StartTurn(h.ctx, owner, sess.ID, "req-2", qa.AskInput{
		Question: "它怎么做到的？",
		Scope:    knowledge.ScopeInput{Type: "kb", KBID: kb.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	evts2 := collectEvents(h.ctx, h2.Stream)
	if !strings.Contains(seenSystem, "Transformer architecture avoids recurrence") {
		t.Fatalf("expected parent chunk context in system prompt, got:\n%s", seenSystem)
	}
	if !strings.Contains(sawRewritePrompt, "Transformer 有什么特点？") || !strings.Contains(sawRewritePrompt, "它怎么做到的？") {
		t.Fatalf("expected history+query in rewrite prompt, got:\n%s", sawRewritePrompt)
	}
	var sawRewriteStage, doneRewritten bool
	for _, e := range evts2 {
		if e.Name == "progress" && decodeEventMap(t, e)["stage"] == "rewrite" {
			sawRewriteStage = true
		}
		if e.Name == "done" {
			doneRewritten = decodeEventMap(t, e)["rewritten"].(bool)
		}
	}
	if !sawRewriteStage || !doneRewritten {
		t.Fatalf("expected rewrite stage and done.rewritten=true, stage=%v done=%v", sawRewriteStage, doneRewritten)
	}

	// 第三轮：改写失败时静默退回原问题继续回答
	h.gw.CompleteFn = func(ctx context.Context, system, user string) (string, error) {
		return "", errors.New("rewrite upstream error")
	}
	h.gw.StreamFn = func(ctx context.Context, system, user string, onDelta func(string) error) error {
		return onDelta("并行训练仍可用 [[c1]]。")
	}
	h3, err := h.svc.StartTurn(h.ctx, owner, sess.ID, "req-3", qa.AskInput{
		Question: "parallel",
		Scope:    knowledge.ScopeInput{Type: "kb", KBID: kb.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	evts3 := collectEvents(h.ctx, h3.Stream)
	last := decodeEventMap(t, evts3[len(evts3)-1])
	if last["rewritten"] != false || last["answer_kind"] != "kb" {
		t.Fatalf("expected rewrite failure fallback to rewritten=false & answer_kind=kb, got %+v", last)
	}
}

func TestScopedGraphSupplementDoesNotLeakOrStealTop5Slots(t *testing.T) {
	h := newHarness(t)
	owner := uuid.NewString()
	sess, err := h.svc.CreateSession(h.ctx, owner, "图谱隔离测试")
	if err != nil {
		t.Fatal(err)
	}
	kbA, err := h.kb.CreateKB(h.ctx, owner, "KB-A")
	if err != nil {
		t.Fatal(err)
	}
	kbB, err := h.kb.CreateKB(h.ctx, owner, "KB-B")
	if err != nil {
		t.Fatal(err)
	}

	c1, c2, c3, c4, c5 := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	cExtraA := uuid.NewString()
	cSuspA := uuid.NewString()
	cLeakB := uuid.NewString()

	docA := seedReadyDoc(t, h.kb, owner, kbA.ID, "Doc-A", 1,
		[]knowledge.MemoryParentChunk{{ID: "p-a", PageStart: 1, Text: "Parent A"}},
		[]knowledge.MemoryChunkInput{
			{ID: c1, ParentID: "p-a", PageStart: 1, Text: "alpha signal one", Embedding: []float32{1, 0}},
			{ID: c2, ParentID: "p-a", PageStart: 2, Text: "alpha signal two", Embedding: []float32{0.99, 0.01}},
			{ID: c3, ParentID: "p-a", PageStart: 3, Text: "alpha signal three", Embedding: []float32{0.98, 0.02}},
			{ID: c4, ParentID: "p-a", PageStart: 4, Text: "alpha signal four", Embedding: []float32{0.97, 0.03}},
			{ID: c5, ParentID: "p-a", PageStart: 5, Text: "alpha signal five", Embedding: []float32{0.96, 0.04}},
			{ID: cExtraA, ParentID: "p-a", PageStart: 6, Text: "beta theorem only via graph", Embedding: []float32{0, 1}},
			{ID: cSuspA, ParentID: "p-a", PageStart: 7, Text: "suspended theorem", Embedding: []float32{0, 1}},
		},
	)
	docB := seedReadyDoc(t, h.kb, owner, kbB.ID, "Doc-B", 1,
		[]knowledge.MemoryParentChunk{{ID: "p-b", PageStart: 1, Text: "Parent B"}},
		[]knowledge.MemoryChunkInput{
			{ID: cLeakB, ParentID: "p-b", PageStart: 9, Text: "foreign secret lemma", Embedding: []float32{0, 1}},
		},
	)

	// c1 同时关联到：同库 cExtraA（有效）、同库挂起 cSuspA（应忽略）、异库 cLeakB（严禁跨库泄漏）
	h.graphMem.SeedRelation(uuid.NewString(), owner, false, []graph.MemoryEvidence{
		{OwnerID: owner, DocumentID: docA, ChunkID: c1, DocReady: true, Generation: 1},
		{OwnerID: owner, DocumentID: docA, ChunkID: cExtraA, DocReady: true, Generation: 1},
		{OwnerID: owner, DocumentID: docB, ChunkID: cLeakB, DocReady: true, Generation: 1},
	})
	h.graphMem.SeedRelation(uuid.NewString(), owner, true, []graph.MemoryEvidence{
		{OwnerID: owner, DocumentID: docA, ChunkID: c1, DocReady: true, Generation: 1},
		{OwnerID: owner, DocumentID: docA, ChunkID: cSuspA, DocReady: true, Generation: 1},
	})

	h.gw.SetModel("chat", provider.Remote{Kind: "chat", Model: "chat-v1"})
	h.gw.SetModel("embedding", provider.Remote{Kind: "embedding", Model: "emb-v1"})
	h.gw.EmbedFn = func(ctx context.Context, inputs []string) ([][]float32, error) {
		return [][]float32{{1, 0}}, nil
	}
	var seenPrompt string
	h.gw.StreamFn = func(ctx context.Context, system, user string, onDelta func(string) error) error {
		seenPrompt = system
		return onDelta("主证据来自 [[c1]]，图谱补充见 [[g1]]。")
	}

	handle, err := h.svc.StartTurn(h.ctx, owner, sess.ID, "req-graph", qa.AskInput{
		Question: "alpha signal",
		Scope:    knowledge.ScopeInput{Type: "kb", KBID: kbA.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	evts := collectEvents(h.ctx, handle.Stream)

	if strings.Contains(seenPrompt, "foreign secret lemma") || strings.Contains(seenPrompt, "suspended theorem") {
		t.Fatalf("graph leaked foreign KB or suspended evidence into prompt:\n%s", seenPrompt)
	}
	if !strings.Contains(seenPrompt, "beta theorem only via graph") {
		t.Fatalf("expected valid graph supplement in prompt:\n%s", seenPrompt)
	}

	var retrievalCount, cCount, gCount int
	var citations []map[string]any
	for _, e := range evts {
		if e.Name == "retrieval" {
			items := decodeEventMap(t, e)["items"].([]any)
			retrievalCount = len(items)
			for _, raw := range items {
				it := raw.(map[string]any)
				if it["graph_supplement"].(bool) {
					gCount++
					if it["chunk_id"] != cExtraA {
						t.Fatalf("unexpected graph chunk %v", it["chunk_id"])
					}
				} else {
					cCount++
				}
			}
		}
		if e.Name == "citations" {
			for _, raw := range decodeEventMap(t, e)["items"].([]any) {
				citations = append(citations, raw.(map[string]any))
			}
		}
	}
	if cCount != 5 || gCount != 1 || retrievalCount != 6 {
		t.Fatalf("expected 5 search slots + 1 graph slot = 6, got c=%d g=%d total=%d", cCount, gCount, retrievalCount)
	}
	if len(citations) != 2 || citations[0]["graph_supplement"] != false || citations[1]["graph_supplement"] != true {
		t.Fatalf("unexpected citations: %+v", citations)
	}

	// 关闭 KB-A 的 graph_enabled 后再次提问，不再发送 progress(graph) 与 g1
	if _, err := h.kb.PatchKB(h.ctx, owner, kbA.ID, map[string]json.RawMessage{
		"graph_enabled": json.RawMessage("false"),
	}); err != nil {
		t.Fatal(err)
	}
	h.gw.StreamFn = func(ctx context.Context, system, user string, onDelta func(string) error) error {
		return onDelta("仅直接引用 [[c1]]。")
	}
	handle2, err := h.svc.StartTurn(h.ctx, owner, sess.ID, "req-nograph", qa.AskInput{
		Question: "alpha signal",
		Scope:    knowledge.ScopeInput{Type: "kb", KBID: kbA.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	evts2 := collectEvents(h.ctx, handle2.Stream)
	for _, e := range evts2 {
		if e.Name == "progress" && decodeEventMap(t, e)["stage"] == "graph" {
			t.Fatalf("graph_enabled=false must skip graph stage")
		}
	}
}

func TestHallucinatedAliasAndRefuseReplace(t *testing.T) {
	h := newHarness(t)
	owner := uuid.NewString()
	sess, err := h.svc.CreateSession(h.ctx, owner, "拒答测试")
	if err != nil {
		t.Fatal(err)
	}
	kb, err := h.kb.CreateKB(h.ctx, owner, "KB")
	if err != nil {
		t.Fatal(err)
	}
	seedReadyDoc(t, h.kb, owner, kb.ID, "Doc", 1,
		[]knowledge.MemoryParentChunk{{ID: "p1", PageStart: 1, Text: "Topic alpha"}},
		[]knowledge.MemoryChunkInput{{ID: uuid.NewString(), ParentID: "p1", PageStart: 1, Text: "Topic alpha detail", Embedding: []float32{1, 0}}},
	)
	h.gw.SetModel("chat", provider.Remote{Kind: "chat", Model: "chat-v1"})
	h.gw.SetModel("embedding", provider.Remote{Kind: "embedding", Model: "emb-v1"})
	h.gw.EmbedFn = func(ctx context.Context, inputs []string) ([][]float32, error) {
		return [][]float32{{1, 0}}, nil
	}

	// Case 1: 编造不存在的别名 [[c99]] 且无有效引用 -> 强制 replace 拒答
	h.gw.StreamFn = func(ctx context.Context, system, user string, onDelta func(string) error) error {
		return onDelta("胡乱编造的答案 [[c99]]。")
	}
	h1, err := h.svc.StartTurn(h.ctx, owner, sess.ID, "req-hallucinate", qa.AskInput{
		Question: "Topic alpha",
		Scope:    knowledge.ScopeInput{Type: "kb", KBID: kb.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	evts1 := collectEvents(h.ctx, h1.Stream)
	var sawReplace bool
	for _, e := range evts1 {
		if e.Name == "replace" && decodeEventMap(t, e)["text"] == qa.Refusal {
			sawReplace = true
		}
	}
	if !sawReplace {
		t.Fatalf("expected replace refusal on hallucinated alias, got %+v", evts1)
	}

	// Case 2: 0 命中直接拒答，且不调用 chat 流
	var streamCalled bool
	h.gw.EmbedFn = func(ctx context.Context, inputs []string) ([][]float32, error) {
		return [][]float32{{0, 1}}, nil
	}
	h.gw.StreamFn = func(ctx context.Context, system, user string, onDelta func(string) error) error {
		streamCalled = true
		return nil
	}
	h2, err := h.svc.StartTurn(h.ctx, owner, sess.ID, "req-zero-hit", qa.AskInput{
		Question: "completely unrelated keyword xyz",
		Scope:    knowledge.ScopeInput{Type: "kb", KBID: kb.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	evts2 := collectEvents(h.ctx, h2.Stream)
	if streamCalled {
		t.Fatal("0-hit query must refuse without calling chat stream")
	}
	if len(evts2) == 0 || decodeEventMap(t, evts2[len(evts2)-1])["answer_kind"] != "refused" {
		t.Fatalf("expected refused turn for 0-hit query, got %+v", evts2)
	}
}

func TestStopSessionBeforeAndAfterTokensNeverPersistsPartialTurn(t *testing.T) {
	h := newHarness(t)
	owner := uuid.NewString()
	otherOwner := uuid.NewString()
	sess, err := h.svc.CreateSession(h.ctx, owner, "停止测试")
	if err != nil {
		t.Fatal(err)
	}
	kb, err := h.kb.CreateKB(h.ctx, owner, "KB")
	if err != nil {
		t.Fatal(err)
	}
	seedReadyDoc(t, h.kb, owner, kb.ID, "Doc", 1,
		nil,
		[]knowledge.MemoryChunkInput{{ID: uuid.NewString(), PageStart: 1, Text: "quantum key distribution"}},
	)
	h.gw.SetModel("chat", provider.Remote{Kind: "chat", Model: "chat-v1"})

	// Case 1: 首 token 前取消
	started := make(chan struct{})
	h.gw.StreamFn = func(ctx context.Context, system, user string, onDelta func(string) error) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}
	handle1, err := h.svc.StartTurn(h.ctx, owner, sess.ID, "req-stop-0", qa.AskInput{
		Question: "quantum key",
		Scope:    knowledge.ScopeInput{Type: "kb", KBID: kb.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	// 他人无权停止该会话
	if err := h.svc.StopSession(h.ctx, otherOwner, sess.ID); !errors.Is(err, apperr.ErrNotFound) {
		t.Fatalf("expected 404 stopping foreign session, got %v", err)
	}
	if err := h.svc.StopSession(h.ctx, owner, sess.ID); err != nil {
		t.Fatal(err)
	}
	evts1 := collectEvents(h.ctx, handle1.Stream)
	var sawCancelled bool
	for _, e := range evts1 {
		if e.Name == "error" && decodeEventMap(t, e)["code"] == "CANCELLED" {
			sawCancelled = true
		}
		if e.Name == "done" {
			t.Fatal("cancelled turn must never emit done")
		}
	}
	if !sawCancelled {
		t.Fatalf("expected CANCELLED error event, got %+v", evts1)
	}

	// Case 2: 已吐出部分 token 后取消 -> 依然绝不落库半成品
	partialEmitted := make(chan struct{})
	h.gw.StreamFn = func(ctx context.Context, system, user string, onDelta func(string) error) error {
		if err := onDelta("已生成部分回答 "); err != nil {
			return err
		}
		close(partialEmitted)
		<-ctx.Done()
		return ctx.Err()
	}
	handle2, err := h.svc.StartTurn(h.ctx, owner, sess.ID, "req-stop-1", qa.AskInput{
		Question: "quantum key",
		Scope:    knowledge.ScopeInput{Type: "kb", KBID: kb.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	<-partialEmitted
	if err := h.svc.StopSession(h.ctx, owner, sess.ID); err != nil {
		t.Fatal(err)
	}
	evts2 := collectEvents(h.ctx, handle2.Stream)
	sawCancelled = false
	for _, e := range evts2 {
		if e.Name == "error" && decodeEventMap(t, e)["code"] == "CANCELLED" {
			sawCancelled = true
		}
	}
	if !sawCancelled {
		t.Fatalf("expected CANCELLED after partial tokens, got %+v", evts2)
	}

	detail, err := h.svc.GetSession(h.ctx, owner, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Turns) != 0 {
		t.Fatalf("expected 0 persisted turns after stop, got %d", len(detail.Turns))
	}
}

func TestStreamResumeMonotonicIDsAndExpiryWindow(t *testing.T) {
	h := newHarness(t, qa.WithStreamTTL(15*time.Minute))
	owner := uuid.NewString()
	sess, err := h.svc.CreateSession(h.ctx, owner, "续传测试")
	if err != nil {
		t.Fatal(err)
	}
	kb, err := h.kb.CreateKB(h.ctx, owner, "KB")
	if err != nil {
		t.Fatal(err)
	}
	seedReadyDoc(t, h.kb, owner, kb.ID, "Resume Doc", 1,
		nil,
		[]knowledge.MemoryChunkInput{{ID: uuid.NewString(), PageStart: 1, Text: "streaming resume mechanism"}},
	)
	h.gw.SetModel("chat", provider.Remote{Kind: "chat", Model: "chat-v1"})

	firstDone := make(chan struct{})
	releaseSecond := make(chan struct{})
	h.gw.StreamFn = func(ctx context.Context, system, user string, onDelta func(string) error) error {
		if err := onDelta("第一段"); err != nil {
			return err
		}
		close(firstDone)
		<-releaseSecond
		return onDelta("第二段 [[c1]]。")
	}

	handle, err := h.svc.StartTurn(h.ctx, owner, sess.ID, "req-resume", qa.AskInput{
		Question: "streaming resume",
		Scope:    knowledge.ScopeInput{Type: "kb", KBID: kb.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	<-firstDone

	// 初始 HTTP 连接读取到第 1 个 token 后断开
	sub1Ctx, cancelSub1 := context.WithCancel(h.ctx)
	var beforeCut []qa.StreamEvent
	for {
		evt, ok := handle.Stream.Next(sub1Ctx)
		if !ok {
			t.Fatal("unexpected stream close before cut")
		}
		beforeCut = append(beforeCut, evt)
		if evt.Name == "token" {
			cancelSub1()
			break
		}
	}
	lastSeq := beforeCut[len(beforeCut)-1].Seq

	// 释放剩余 token，通过 ResumeTurn(afterSeq = lastSeq) 续传
	close(releaseSecond)
	sub2, err := h.svc.ResumeTurn(h.ctx, owner, sess.ID, handle.TurnID, lastSeq)
	if err != nil {
		t.Fatal(err)
	}
	afterCut := collectEvents(h.ctx, sub2)
	combined := append(beforeCut, afterCut...)
	for i, e := range combined {
		if e.Seq != int64(i+1) {
			t.Fatalf("expected contiguous monotonic seq %d, got %d (%s)", i+1, e.Seq, e.Name)
		}
	}
	if combined[len(combined)-1].Name != "done" {
		t.Fatalf("expected final done event, got %s", combined[len(combined)-1].Name)
	}

	// 10 分钟后（未过 15m）仍可从头重放
	t10 := h.advance(10 * time.Minute)
	if n := h.svc.SweepExpiredStreams(t10); n != 0 {
		t.Fatalf("expected 0 expired before 15m, got %d", n)
	}
	if _, err := h.svc.ResumeTurn(h.ctx, owner, sess.ID, handle.TurnID, 0); err != nil {
		t.Fatalf("expected replay before 15m, got %v", err)
	}

	// 满 15 分钟清理后，同轮存在但缓冲已过期 -> 409 STREAM_CLOSED
	t16 := h.advance(6 * time.Minute)
	if n := h.svc.SweepExpiredStreams(t16); n != 1 {
		t.Fatalf("expected 1 expired after 15m, got %d", n)
	}
	_, err = h.svc.ResumeTurn(h.ctx, owner, sess.ID, handle.TurnID, 0)
	if !errors.Is(err, apperr.ErrStreamClosed) {
		t.Fatalf("expected STREAM_CLOSED after TTL expiry, got %v", err)
	}

	// 不存在的 turn_id -> 404 NOT_FOUND
	_, err = h.svc.ResumeTurn(h.ctx, owner, sess.ID, uuid.NewString(), 0)
	if !errors.Is(err, apperr.ErrNotFound) {
		t.Fatalf("expected NOT_FOUND for unknown turn_id, got %v", err)
	}
}

func TestDeletedDocumentNormalizesCitationLabel(t *testing.T) {
	h := newHarness(t)
	owner := uuid.NewString()
	sess, err := h.svc.CreateSession(h.ctx, owner, "引用失效测试")
	if err != nil {
		t.Fatal(err)
	}
	kb, err := h.kb.CreateKB(h.ctx, owner, "KB")
	if err != nil {
		t.Fatal(err)
	}
	docID := seedReadyDoc(t, h.kb, owner, kb.ID, "Temp Paper", 1,
		nil,
		[]knowledge.MemoryChunkInput{{ID: uuid.NewString(), PageStart: 3, Text: "ephemeral fact"}},
	)
	h.gw.SetModel("chat", provider.Remote{Kind: "chat", Model: "chat-v1"})
	h.gw.StreamFn = func(ctx context.Context, system, user string, onDelta func(string) error) error {
		return onDelta("结论见 [[c1]]。")
	}

	handle, err := h.svc.StartTurn(h.ctx, owner, sess.ID, "req-del", qa.AskInput{
		Question: "ephemeral fact",
		Scope:    knowledge.ScopeInput{Type: "kb", KBID: kb.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = collectEvents(h.ctx, handle.Stream)

	// 删除文档后再查询会话详情，kb 引用应规范化为 "来源已删除" 且置空 ID
	if err := h.kb.Delete(h.ctx, owner, docID); err != nil {
		t.Fatal(err)
	}
	detail, err := h.svc.GetSession(h.ctx, owner, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Turns) != 1 || len(detail.Turns[0].Citations) != 1 {
		t.Fatalf("expected 1 turn & 1 citation, got %+v", detail.Turns)
	}
	c := detail.Turns[0].Citations[0]
	if c.Label != "来源已删除" || c.ChunkID != nil || c.DocumentID != nil {
		t.Fatalf("expected normalized deleted citation, got %+v", c)
	}
}

func TestAsiaShanghaiTimestampsAndDoneSessionIDAcrossBranches(t *testing.T) {
	h := newHarness(t)
	owner := uuid.NewString()
	sess, err := h.svc.CreateSession(h.ctx, owner, "时区与Done校验")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(sess.CreatedAt, "+08:00") || !strings.HasSuffix(sess.UpdatedAt, "+08:00") {
		t.Fatalf("CreateSession timestamps must use +08:00, got created=%q updated=%q", sess.CreatedAt, sess.UpdatedAt)
	}

	listed, _, err := h.svc.ListSessions(h.ctx, owner, 1, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || !strings.HasSuffix(listed[0].CreatedAt, "+08:00") || !strings.HasSuffix(listed[0].UpdatedAt, "+08:00") {
		t.Fatalf("ListSessions timestamps must use +08:00, got %+v", listed)
	}

	kb, err := h.kb.CreateKB(h.ctx, owner, "KB")
	if err != nil {
		t.Fatal(err)
	}
	seedReadyDoc(t, h.kb, owner, kb.ID, "Paper", 1, nil,
		[]knowledge.MemoryChunkInput{{ID: uuid.NewString(), PageStart: 2, Text: "attention mechanism foundation", Embedding: []float32{1, 0}}},
	)
	h.gw.SetModel("chat", provider.Remote{Kind: "chat", Model: "chat-v1"})
	h.gw.SetModel("embedding", provider.Remote{Kind: "embedding", Model: "emb-v1"})
	h.gw.EmbedFn = func(ctx context.Context, inputs []string) ([][]float32, error) {
		return [][]float32{{1, 0}}, nil
	}

	// 1. 正常回答 done 带 session_id
	h.gw.StreamFn = func(ctx context.Context, system, user string, onDelta func(string) error) error {
		return onDelta("基于 [[c1]] 的回答。")
	}
	hNorm, err := h.svc.StartTurn(h.ctx, owner, sess.ID, "req-norm", qa.AskInput{
		Question: "attention mechanism",
		Scope:    knowledge.ScopeInput{Type: "kb", KBID: kb.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	evtsNorm := collectEvents(h.ctx, hNorm.Stream)
	lastNorm := decodeEventMap(t, evtsNorm[len(evtsNorm)-1])
	if evtsNorm[len(evtsNorm)-1].Name != "done" || lastNorm["session_id"] != sess.ID || lastNorm["answer_kind"] != "kb" {
		t.Fatalf("normal turn done must include session_id=%q, got %s %+v", sess.ID, evtsNorm[len(evtsNorm)-1].Name, lastNorm)
	}

	// 2. 拒答分支 done 带 session_id
	h.gw.StreamFn = func(ctx context.Context, system, user string, onDelta func(string) error) error {
		return onDelta("[[REFUSE]]")
	}
	hRefuse, err := h.svc.StartTurn(h.ctx, owner, sess.ID, "req-refuse", qa.AskInput{
		Question: "attention mechanism",
		Scope:    knowledge.ScopeInput{Type: "kb", KBID: kb.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	evtsRefuse := collectEvents(h.ctx, hRefuse.Stream)
	lastRefuse := decodeEventMap(t, evtsRefuse[len(evtsRefuse)-1])
	if evtsRefuse[len(evtsRefuse)-1].Name != "done" || lastRefuse["session_id"] != sess.ID || lastRefuse["answer_kind"] != "refused" {
		t.Fatalf("refused turn done must include session_id=%q, got %s %+v", sess.ID, evtsRefuse[len(evtsRefuse)-1].Name, lastRefuse)
	}

	detail, err := h.svc.GetSession(h.ctx, owner, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(detail.CreatedAt, "+08:00") || !strings.HasSuffix(detail.UpdatedAt, "+08:00") {
		t.Fatalf("GetSession timestamps must use +08:00, got created=%q updated=%q", detail.CreatedAt, detail.UpdatedAt)
	}
	if len(detail.Turns) != 2 || !strings.HasSuffix(detail.Turns[0].CreatedAt, "+08:00") {
		t.Fatalf("Turn timestamps must use +08:00, got %+v", detail.Turns)
	}
}

func TestPatchDeleteOptionsAndModelErrorSSE(t *testing.T) {
	h := newHarness(t,
		qa.WithTurnTimeout(5*time.Second),
		qa.WithGraphLimit(2),
		qa.WithHistoryLimit(3),
	)
	owner := uuid.NewString()
	kb, err := h.kb.CreateKB(h.ctx, owner, "ErrKB")
	if err != nil {
		t.Fatal(err)
	}
	seedReadyDoc(t, h.kb, owner, kb.ID, "ErrDoc", 1,
		[]knowledge.MemoryParentChunk{{ID: "p1", PageStart: 1, Text: "Parent"}},
		[]knowledge.MemoryChunkInput{{ID: "c1", ParentID: "p1", PageStart: 1, ParagraphIndex: 0, Text: "Alpha method", Embedding: []float32{1, 0, 0, 0}}},
	)
	sess, err := h.svc.CreateSession(h.ctx, owner, "")
	if err != nil {
		t.Fatal(err)
	}
	patched, err := h.svc.PatchSession(h.ctx, owner, sess.ID, "已修改标题")
	if err != nil || patched.Title != "已修改标题" {
		t.Fatalf("PatchSession got %+v err=%v", patched, err)
	}

	h.gw.StreamFn = func(context.Context, string, string, func(string) error) error {
		return apperr.ErrModelDown
	}
	handle, err := h.svc.StartTurn(h.ctx, owner, sess.ID, "req-err-1", qa.AskInput{
		Question: "Alpha method",
		Scope:    knowledge.ScopeInput{Type: "kb", KBID: kb.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	evts := collectEvents(h.ctx, handle.Stream)
	last := evts[len(evts)-1]
	m := decodeEventMap(t, last)
	if last.Name != "error" || m["request_id"] != "req-err-1" {
		t.Fatalf("expected error SSE with request_id, got %s %+v", last.Name, m)
	}

	if err := h.svc.DeleteSession(h.ctx, owner, sess.ID); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	if _, err := h.svc.GetSession(h.ctx, owner, sess.ID); !errors.Is(err, apperr.ErrNotFound) {
		t.Fatalf("expected ErrNotFound after DeleteSession, got %v", err)
	}
}
