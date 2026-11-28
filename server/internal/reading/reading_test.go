package reading_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"yanzhi/server/internal/apperr"
	"yanzhi/server/internal/knowledge"
	"yanzhi/server/internal/provider"
	"yanzhi/server/internal/reading"
)

type spyGateway struct {
	modelConfigured bool
	streamCalls     int
	streamDeltas    []string
	streamErr       error
	lastSystem      string
	lastUser        string
}

func (g *spyGateway) RequireModel(_ context.Context, kind string) (provider.Remote, error) {
	if !g.modelConfigured || kind != "chat" {
		return provider.Remote{}, apperr.ErrModelMissing
	}
	return provider.Remote{Kind: "chat", Model: "mock-chat"}, nil
}

func (g *spyGateway) ActiveGeneration(context.Context) (int64, error) { return 1, nil }
func (g *spyGateway) Embed(context.Context, provider.CallMeta, []string) ([][]float32, error) {
	return nil, nil
}
func (g *spyGateway) Rerank(context.Context, provider.CallMeta, string, []string) ([]int, string) {
	return nil, ""
}
func (g *spyGateway) Complete(context.Context, provider.CallMeta, string, string) (string, error) {
	return "", nil
}
func (g *spyGateway) Stream(_ context.Context, _ provider.CallMeta, system, user string, onDelta func(string) error) error {
	g.streamCalls++
	g.lastSystem = system
	g.lastUser = user
	for _, delta := range g.streamDeltas {
		if err := onDelta(delta); err != nil {
			return err
		}
	}
	return g.streamErr
}

func seedDoc(t *testing.T, kb *knowledge.MemoryStore, owner, kbID, title, status string, pageCount int, chunks []knowledge.MemoryChunkInput) string {
	t.Helper()
	ctx := context.Background()
	pages := strings.Repeat("1 0 obj<< /Type /Page >>endobj\n", pageCount)
	pdf := []byte("%PDF-1.4\n" + pages + "trailer<<>>\n%%EOF")
	res, err := kb.Upload(ctx, owner, kbID, knowledge.UploadMeta{
		FileName: title + ".pdf",
		Title:    title,
	}, pdf)
	if err != nil {
		t.Fatal(err)
	}
	if err := kb.SetDocStatus(owner, res.Doc.ID, status); err != nil {
		t.Fatal(err)
	}
	if len(chunks) > 0 {
		parentID := uuid.NewString()
		if err := kb.SeedChunks(owner, res.Doc.ID, 1, []knowledge.MemoryParentChunk{{
			ID:        parentID,
			PageStart: chunks[0].PageStart,
			Text:      "parent",
		}}, chunks); err != nil {
			t.Fatal(err)
		}
	}
	return res.Doc.ID
}

func TestReadingServiceLifecycleCacheInvalidationAndErrors(t *testing.T) {
	ctx := context.Background()
	kbStore := knowledge.NewMemoryStore(nil, nil, nil)
	owner := uuid.NewString()
	other := uuid.NewString()
	kbRec, err := kbStore.CreateKB(ctx, owner, "kb")
	if err != nil {
		t.Fatal(err)
	}

	pendingID := seedDoc(t, kbStore, owner, kbRec.ID, "PendingDoc", "parsing", 5, nil)
	docID := seedDoc(t, kbStore, owner, kbRec.ID, "PaperTitle", "ready", 6, []knowledge.MemoryChunkInput{
		{PageStart: 4, ParagraphIndex: 2, Text: "Second sentence on page 4."},
		{PageStart: 4, ParagraphIndex: 1, Text: "SENTENCE-P4 first paragraph."},
		{PageStart: 1, ParagraphIndex: 1, Text: "Page one text."},
	})

	store := reading.NewMemoryStore(kbStore)
	gw := &spyGateway{
		modelConfigured: false,
		streamDeltas:    []string{"第四页第一段，", "第四页第二段。"},
	}
	svc := reading.NewService(store, gw)

	// AC-30-06: other user gets 404 ErrNotFound
	if _, err := svc.GetPage(ctx, other, docID, 4); !errors.Is(err, apperr.ErrNotFound) {
		t.Fatalf("expected ErrNotFound for other owner, got %v", err)
	}

	// AC-30-02: non-ready doc returns ErrNotReady
	if _, err := svc.GetPage(ctx, owner, pendingID, 1); !errors.Is(err, apperr.ErrNotReady) {
		t.Fatalf("expected ErrNotReady, got %v", err)
	}

	// Page out of range returns ErrPageOutOfRange (422)
	if _, err := svc.GetPage(ctx, owner, docID, 0); !errors.Is(err, apperr.ErrPageOutOfRange) {
		t.Fatalf("expected ErrPageOutOfRange for page 0, got %v", err)
	}
	if _, err := svc.GetPage(ctx, owner, docID, 7); !errors.Is(err, apperr.ErrPageOutOfRange) {
		t.Fatalf("expected ErrPageOutOfRange for page 7, got %v", err)
	}

	// AC-30-07: empty page (page 5) returns EmptyPageMessage without calling model even if model unconfigured
	emptyPage, err := svc.GetPage(ctx, owner, docID, 5)
	if err != nil || emptyPage.SourceText != "" || emptyPage.Translation != nil || emptyPage.Cached {
		t.Fatalf("unexpected empty page: %+v err=%v", emptyPage, err)
	}
	plan, emptyMsg, err := svc.PrepareTranslation(ctx, reading.TranslateRequest{
		Owner: owner, DocID: docID, Page: 5, RequestID: "r-empty",
	})
	if err != nil || plan != nil || emptyMsg != reading.EmptyPageMessage || gw.streamCalls != 0 {
		t.Fatalf("expected EmptyPageMessage without model call, got plan=%v msg=%q err=%v calls=%d", plan, emptyMsg, err, gw.streamCalls)
	}

	// Model unconfigured returns ErrModelMissing (409) before opening SSE
	if _, _, err := svc.PrepareTranslation(ctx, reading.TranslateRequest{
		Owner: owner, DocID: docID, Page: 4, RequestID: "r-nomodel",
	}); !errors.Is(err, apperr.ErrModelMissing) {
		t.Fatalf("expected ErrModelMissing, got %v", err)
	}

	// Configure model -> stream translation writes cache & emits token + done
	gw.modelConfigured = true
	plan, emptyMsg, err = svc.PrepareTranslation(ctx, reading.TranslateRequest{
		Owner: owner, DocID: docID, Page: 4, RequestID: "r-1",
	})
	if err != nil || emptyMsg != "" || plan == nil || plan.IsCached {
		t.Fatalf("unexpected plan: %+v msg=%q err=%v", plan, emptyMsg, err)
	}
	if !strings.Contains(plan.SourceText, "SENTENCE-P4 first paragraph.") || !strings.Contains(plan.SourceText, "Second sentence on page 4.") {
		t.Fatalf("expected ordered source text, got %q", plan.SourceText)
	}
	var events []string
	var gotText string
	err = svc.StreamTranslation(ctx, plan, func(event string, payload any) error {
		events = append(events, event)
		if m, ok := payload.(map[string]any); ok && event == "done" {
			gotText, _ = m["text"].(string)
		}
		return nil
	})
	if err != nil || gw.streamCalls != 1 || gotText != "第四页第一段，第四页第二段。" {
		t.Fatalf("stream failed: err=%v calls=%d text=%q events=%v", err, gw.streamCalls, gotText, events)
	}
	if strings.Join(events, ",") != "token,token,done" {
		t.Fatalf("unexpected event sequence: %v", events)
	}

	// AC-30-03: GetPage & PrepareTranslation return cache without calling model
	page4, err := svc.GetPage(ctx, owner, docID, 4)
	if err != nil || !page4.Cached || page4.Translation == nil || *page4.Translation != "第四页第一段，第四页第二段。" {
		t.Fatalf("expected cached page4, got %+v err=%v", page4, err)
	}
	planCached, _, err := svc.PrepareTranslation(ctx, reading.TranslateRequest{
		Owner: owner, DocID: docID, Page: 4, RequestID: "r-cached",
	})
	if err != nil || planCached == nil || !planCached.IsCached {
		t.Fatalf("expected cached plan, got %+v err=%v", planCached, err)
	}
	events = nil
	if err := svc.StreamTranslation(ctx, planCached, func(event string, _ any) error {
		events = append(events, event)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if gw.streamCalls != 1 || strings.Join(events, ",") != "cached,done" {
		t.Fatalf("expected cache hit without model call, calls=%d events=%v", gw.streamCalls, events)
	}

	// AC-30-04: Revising page 4 chunks invalidates old translation & triggers regeneration
	if err := kbStore.SeedChunks(owner, docID, 1, []knowledge.MemoryParentChunk{{
		PageStart: 4, Text: "parent",
	}}, []knowledge.MemoryChunkInput{
		{PageStart: 4, ParagraphIndex: 1, Text: "REVISED-P4 paragraph."},
	}); err != nil {
		t.Fatal(err)
	}
	revisedPage4, err := svc.GetPage(ctx, owner, docID, 4)
	if err != nil || revisedPage4.Cached || revisedPage4.Translation != nil {
		t.Fatalf("expected stale translation cleared after revision, got %+v err=%v", revisedPage4, err)
	}

	gw.streamDeltas = []string{"修订后的译文。"}
	planRevised, _, err := svc.PrepareTranslation(ctx, reading.TranslateRequest{
		Owner: owner, DocID: docID, Page: 4, RequestID: "r-revised",
	})
	if err != nil || planRevised == nil || planRevised.IsCached {
		t.Fatalf("expected fresh translation plan after revision, got %+v err=%v", planRevised, err)
	}
	if err := svc.StreamTranslation(ctx, planRevised, func(string, any) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if gw.streamCalls != 2 {
		t.Fatalf("expected 2 model calls after revision, got %d", gw.streamCalls)
	}
}

func TestReadingServiceOverlapTrimAndStreamFailureDoesNotCache(t *testing.T) {
	ctx := context.Background()
	text, hash := reading.AssemblePageText([]reading.PageChunk{
		{Text: "Alpha beta", OverlapPrefix: 0},
		{Text: "beta gamma", OverlapPrefix: 4},
	})
	if text != "Alpha beta\n\ngamma" || len(hash) != 64 {
		t.Fatalf("unexpected assembled text=%q hash=%q", text, hash)
	}

	kbStore := knowledge.NewMemoryStore(nil, nil, nil)
	owner := uuid.NewString()
	kbRec, _ := kbStore.CreateKB(ctx, owner, "kb")
	docID := seedDoc(t, kbStore, owner, kbRec.ID, "FailDoc", "ready", 2, []knowledge.MemoryChunkInput{
		{PageStart: 1, ParagraphIndex: 1, Text: "Paragraph one."},
	})
	gw := &spyGateway{
		modelConfigured: true,
		streamDeltas:    []string{"partial "},
		streamErr:       apperr.ErrModelDown,
	}
	svc := reading.NewService(reading.NewMemoryStore(kbStore), gw)
	plan, _, err := svc.PrepareTranslation(ctx, reading.TranslateRequest{Owner: owner, DocID: docID, Page: 1, RequestID: "r-fail"})
	if err != nil {
		t.Fatal(err)
	}
	var events []string
	err = svc.StreamTranslation(ctx, plan, func(event string, _ any) error {
		events = append(events, event)
		return nil
	})
	if !errors.Is(err, apperr.ErrModelDown) || strings.Join(events, ",") != "token,error" {
		t.Fatalf("expected ErrModelDown and token,error events; got err=%v events=%v", err, events)
	}
	page, err := svc.GetPage(ctx, owner, docID, 1)
	if err != nil || page.Cached || page.Translation != nil {
		t.Fatalf("partial stream failure must not cache translation: %+v err=%v", page, err)
	}
}
