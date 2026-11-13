package knowledge

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"yanzhi/server/internal/apperr"
)

type mockStorageCoordinator struct {
	bytes map[string]int64
}

func newMockStorage() *mockStorageCoordinator {
	return &mockStorageCoordinator{bytes: make(map[string]int64)}
}

func (m *mockStorageCoordinator) GetStorageBytes(userID string) (int64, error) {
	return m.bytes[userID], nil
}

func (m *mockStorageCoordinator) ReserveStorage(userID string, delta, quotaBytes int64) error {
	used := m.bytes[userID]
	if used+delta > quotaBytes {
		return apperr.Quota(used)
	}
	m.bytes[userID] = used + delta
	return nil
}

func (m *mockStorageCoordinator) ReleaseStorage(userID string, delta int64) error {
	used := m.bytes[userID] - delta
	if used < 0 {
		used = 0
	}
	m.bytes[userID] = used
	return nil
}

type manualClock struct {
	now time.Time
}

func (c *manualClock) Now() time.Time {
	return c.now
}

func samplePDF() []byte {
	return []byte("%PDF-1.4\n1 0 obj\n<< /Type /Catalog >>\nendobj\ntrailer\n<< /Root 1 0 R >>\n%%EOF")
}

func sampleMultiPagePDF(pages int) []byte {
	var b strings.Builder
	b.WriteString("%PDF-1.4\n")
	for i := 1; i <= pages; i++ {
		b.WriteString("/Type /Page\n")
	}
	b.WriteString("%%EOF")
	return []byte(b.String())
}

func TestKBManagement(t *testing.T) {
	store := NewMemoryStore(newMockStorage(), nil, nil)
	ctx := context.Background()
	owner := uuid.NewString()

	kbs, err := store.ListKBs(ctx, owner)
	if err != nil || len(kbs) != 0 {
		t.Fatalf("expected empty kbs, got %v, err: %v", kbs, err)
	}

	_, err = store.CreateKB(ctx, owner, "")
	var appErr *apperr.Error
	if !errors.As(err, &appErr) || appErr.Code != "VALIDATION_ERROR" {
		t.Fatalf("expected validation error for empty name, got %v", err)
	}

	_, err = store.CreateKB(ctx, owner, strings.Repeat("a", 51))
	if !errors.As(err, &appErr) || appErr.Code != "VALIDATION_ERROR" {
		t.Fatalf("expected validation error for long name, got %v", err)
	}

	kb, err := store.CreateKB(ctx, owner, "论文阅读")
	if err != nil {
		t.Fatal(err)
	}
	if kb.Name != "论文阅读" || !kb.GraphEnabled {
		t.Fatalf("unexpected kb: %+v", kb)
	}

	got, err := store.GetKB(ctx, owner, kb.ID)
	if err != nil || got.ID != kb.ID || got.Name != "论文阅读" {
		t.Fatalf("expected kb %s, got %+v, err: %v", kb.ID, got, err)
	}

	otherOwner := uuid.NewString()
	_, err = store.GetKB(ctx, otherOwner, kb.ID)
	if !errors.Is(err, apperr.ErrNotFound) {
		t.Fatalf("expected 404 for other owner, got %v", err)
	}

	list, err := store.ListKBs(ctx, owner)
	if err != nil || len(list) != 1 || list[0].ID != kb.ID {
		t.Fatalf("expected 1 kb in list, got %+v", list)
	}
}

func TestUploadRulesAndFlow(t *testing.T) {
	storage := newMockStorage()
	clk := &manualClock{now: time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)}
	store := NewMemoryStore(storage, nil, clk)
	ctx := context.Background()
	owner := uuid.NewString()

	kb, err := store.CreateKB(ctx, owner, "AI Papers")
	if err != nil {
		t.Fatal(err)
	}

	meta := UploadMeta{
		FileName: "test.docx",
		Title:    "Test Doc",
	}
	_, err = store.Upload(ctx, owner, kb.ID, meta, samplePDF())
	var appErr *apperr.Error
	if !errors.As(err, &appErr) || appErr.Code != "UNSUPPORTED_MEDIA" {
		t.Fatalf("expected UNSUPPORTED_MEDIA for docx extension, got %v", err)
	}

	meta.FileName = "fake.pdf"
	_, err = store.Upload(ctx, owner, kb.ID, meta, []byte("NOT-A-PDF-HEADER"))
	if !errors.As(err, &appErr) || appErr.Code != "UNSUPPORTED_MEDIA" {
		t.Fatalf("expected UNSUPPORTED_MEDIA for non-pdf header, got %v", err)
	}

	meta.FileName = "test.pdf"
	_, err = store.Upload(ctx, owner, uuid.NewString(), meta, samplePDF())
	if !errors.Is(err, apperr.ErrNotFound) {
		t.Fatalf("expected NOT_FOUND for unknown KB, got %v", err)
	}

	otherOwner := uuid.NewString()
	_, err = store.Upload(ctx, otherOwner, kb.ID, meta, samplePDF())
	if !errors.Is(err, apperr.ErrNotFound) {
		t.Fatalf("expected NOT_FOUND for foreign KB, got %v", err)
	}

	huge := make([]byte, apperr.MaxUploadBytes+1)
	copy(huge, samplePDF())
	_, err = store.Upload(ctx, owner, kb.ID, meta, huge)
	if !errors.As(err, &appErr) || appErr.Code != "FILE_TOO_LARGE" {
		t.Fatalf("expected FILE_TOO_LARGE, got %v", err)
	}

	tooManyPages := sampleMultiPagePDF(301)
	_, err = store.Upload(ctx, owner, kb.ID, meta, tooManyPages)
	if !errors.As(err, &appErr) || appErr.Code != "TOO_MANY_PAGES" {
		t.Fatalf("expected TOO_MANY_PAGES, got %v", err)
	}

	encrypted := []byte("%PDF-1.4\n/Encrypt 2 0 R\n%%EOF")
	_, err = store.Upload(ctx, owner, kb.ID, meta, encrypted)
	if !errors.As(err, &appErr) || appErr.Code != "FILE_ENCRYPTED" {
		t.Fatalf("expected FILE_ENCRYPTED, got %v", err)
	}

	storage.bytes[owner] = 2137000000
	twentyMB := make([]byte, 20971520)
	copy(twentyMB, samplePDF())
	_, err = store.Upload(ctx, owner, kb.ID, meta, twentyMB)
	if !errors.As(err, &appErr) || appErr.Code != "STORAGE_QUOTA_EXCEEDED" {
		t.Fatalf("expected STORAGE_QUOTA_EXCEEDED, got %v", err)
	}
	storage.bytes[owner] = 0

	meta.Title = "Valid Paper"
	year := 2024
	meta.Year = &year
	meta.Authors = []string{"Alice", "Bob"}
	meta.IdempotencyKey = "key-1"

	res, err := store.Upload(ctx, owner, kb.ID, meta, samplePDF())
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != 201 {
		t.Fatalf("expected 201, got %d", res.Status)
	}
	if res.Doc.Title != "Valid Paper" || res.Doc.Status != "待处理" {
		t.Fatalf("unexpected doc: %+v", res.Doc)
	}

	replay, err := store.Upload(ctx, owner, kb.ID, meta, samplePDF())
	if err != nil || replay.Status != 201 || replay.Doc.ID != res.Doc.ID {
		t.Fatalf("expected idempotency replay, got status %d, err %v", replay.Status, err)
	}

	conflictMeta := meta
	conflictMeta.Title = "Different Title"
	_, err = store.Upload(ctx, owner, kb.ID, conflictMeta, samplePDF())
	if !errors.As(err, &appErr) || appErr.Code != "IDEMPOTENCY_CONFLICT" {
		t.Fatalf("expected IDEMPOTENCY_CONFLICT, got %v", err)
	}

	metaNoKey := meta
	metaNoKey.IdempotencyKey = ""
	_, err = store.Upload(ctx, owner, kb.ID, metaNoKey, samplePDF())
	if !errors.As(err, &appErr) || appErr.Code != "DUPLICATE_DOCUMENT" {
		t.Fatalf("expected DUPLICATE_DOCUMENT, got %v", err)
	}
	if appErr.Details["document_id"] != res.Doc.ID || appErr.Details["kb_id"] != kb.ID {
		t.Fatalf("expected duplicate doc details, got %+v", appErr.Details)
	}

	failReason := "parse error"
	if err := store.SetDocStatusForTest(owner, res.Doc.ID, "failed", &failReason); err != nil {
		t.Fatal(err)
	}

	metaFailed := meta
	metaFailed.Title = "Reused Title"
	metaFailed.IdempotencyKey = "key-reused"
	reused, err := store.Upload(ctx, owner, kb.ID, metaFailed, samplePDF())
	if err != nil {
		t.Fatal(err)
	}
	if reused.Status != 200 {
		t.Fatalf("expected 200 for failed reuse, got %d", reused.Status)
	}
	if reused.Doc.ID != res.Doc.ID || reused.Doc.Status != "待处理" || reused.Doc.Title != "Reused Title" {
		t.Fatalf("failed doc not reused properly: %+v", reused.Doc)
	}
}

func TestDocListDetailPatchDelete(t *testing.T) {
	store := NewMemoryStore(newMockStorage(), nil, nil)
	ctx := context.Background()
	owner := uuid.NewString()

	kb, err := store.CreateKB(ctx, owner, "Papers")
	if err != nil {
		t.Fatal(err)
	}

	meta := UploadMeta{
		FileName: "paper.pdf",
		Title:    "图谱论文一",
	}
	res1, err := store.Upload(ctx, owner, kb.ID, meta, samplePDF())
	if err != nil {
		t.Fatal(err)
	}

	pdf2 := append(samplePDF(), []byte("\n% second doc")...)
	meta.Title = "旧标题"
	res2, err := store.Upload(ctx, owner, kb.ID, meta, pdf2)
	if err != nil {
		t.Fatal(err)
	}

	pdf3 := append(samplePDF(), []byte("\n% third doc")...)
	meta.Title = "图谱论文三"
	res3, err := store.Upload(ctx, owner, kb.ID, meta, pdf3)
	if err != nil {
		t.Fatal(err)
	}

	detail, err := store.GetDoc(ctx, owner, res2.Doc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Title != "旧标题" || detail.HasPhysicalPages == nil || !*detail.HasPhysicalPages {
		t.Fatalf("unexpected detail: %+v", detail)
	}

	otherOwner := uuid.NewString()
	_, err = store.GetDoc(ctx, otherOwner, res2.Doc.ID)
	if !errors.Is(err, apperr.ErrNotFound) {
		t.Fatalf("expected 404 for foreign doc, got %v", err)
	}

	patchRaw := map[string]json.RawMessage{
		"title":   json.RawMessage(`"新标题"`),
		"venue":   json.RawMessage(`"NeurIPS 2026"`),
		"authors": json.RawMessage(`["Zhang", "Li"]`),
	}
	patched, err := store.PatchDoc(ctx, owner, res2.Doc.ID, patchRaw)
	if err != nil {
		t.Fatal(err)
	}
	if patched.Title != "新标题" || patched.Venue == nil || *patched.Venue != "NeurIPS 2026" || len(patched.Authors) != 2 {
		t.Fatalf("unexpected patched doc: %+v", patched)
	}

	title, fileBytes, err := store.File(ctx, owner, res2.Doc.ID)
	if err != nil || title != "新标题" || len(fileBytes) != len(pdf2) {
		t.Fatalf("download failed: title %s, len %d, err %v", title, len(fileBytes), err)
	}

	_, _, err = store.File(ctx, otherOwner, res2.Doc.ID)
	if !errors.Is(err, apperr.ErrNotFound) {
		t.Fatalf("expected 404 downloading foreign file, got %v", err)
	}

	docs, total, err := store.ListDocs(ctx, owner, kb.ID, "图谱", nil, 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 || len(docs) != 2 {
		t.Fatalf("expected 2 docs matching '图谱', got total %d, items %d", total, len(docs))
	}
	for _, d := range docs {
		if d.ID != res1.Doc.ID && d.ID != res3.Doc.ID {
			t.Fatalf("unexpected doc in search results: %s", d.Title)
		}
	}

	if err := store.Delete(ctx, otherOwner, res1.Doc.ID); !errors.Is(err, apperr.ErrNotFound) {
		t.Fatalf("expected 404 deleting foreign doc, got %v", err)
	}

	if err := store.Delete(ctx, owner, res1.Doc.ID); err != nil {
		t.Fatal(err)
	}

	_, err = store.GetDoc(ctx, owner, res1.Doc.ID)
	if !errors.Is(err, apperr.ErrNotFound) {
		t.Fatalf("expected 404 after deletion, got %v", err)
	}

	resReupload, err := store.Upload(ctx, owner, kb.ID, UploadMeta{FileName: "paper.pdf", Title: "Reuploaded"}, samplePDF())
	if err != nil {
		t.Fatalf("reupload after deletion should succeed, got %v", err)
	}
	if resReupload.Status != 201 {
		t.Fatalf("expected 201, got %d", resReupload.Status)
	}
}

func TestTagOperations(t *testing.T) {
	store := NewMemoryStore(newMockStorage(), nil, nil)
	ctx := context.Background()
	owner := uuid.NewString()

	tags, err := store.ListTags(ctx, owner)
	if err != nil || len(tags) != 0 {
		t.Fatalf("expected empty tags, got %+v", tags)
	}

	_, err = store.CreateTag(ctx, owner, "")
	var appErr *apperr.Error
	if !errors.As(err, &appErr) || appErr.Code != "VALIDATION_ERROR" {
		t.Fatalf("expected VALIDATION_ERROR for empty tag name, got %v", err)
	}

	t1, err := store.CreateTag(ctx, owner, "检索")
	if err != nil || t1.Name != "检索" {
		t.Fatalf("failed creating tag: %+v, err: %v", t1, err)
	}

	_, err = store.CreateTag(ctx, owner, "检索")
	if !errors.As(err, &appErr) || appErr.Code != "TAG_EXISTS" {
		t.Fatalf("expected TAG_EXISTS, got %v", err)
	}

	t2, err := store.CreateTag(ctx, owner, "图神经网络")
	if err != nil {
		t.Fatal(err)
	}

	list, err := store.ListTags(ctx, owner)
	if err != nil || len(list) != 2 {
		t.Fatalf("expected 2 tags, got %+v", list)
	}

	otherOwner := uuid.NewString()
	tOther, err := store.CreateTag(ctx, otherOwner, "检索")
	if err != nil || tOther.Name != "检索" {
		t.Fatalf("different owner can have tag with same name: %v", err)
	}

	kb, err := store.CreateKB(ctx, owner, "KB")
	if err != nil {
		t.Fatal(err)
	}
	docRes, err := store.Upload(ctx, owner, kb.ID, UploadMeta{FileName: "a.pdf", Title: "Doc A"}, samplePDF())
	if err != nil {
		t.Fatal(err)
	}

	docTagged, err := store.PutTags(ctx, owner, docRes.Doc.ID, []string{t1.ID, t2.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(docTagged.Tags) != 2 {
		t.Fatalf("expected 2 tags assigned, got %+v", docTagged.Tags)
	}

	_, err = store.PutTags(ctx, owner, docRes.Doc.ID, []string{tOther.ID})
	if !errors.Is(err, apperr.ErrNotFound) {
		t.Fatalf("cannot attach foreign tag, expected 404, got %v", err)
	}

	docFiltered, total, err := store.ListDocs(ctx, owner, kb.ID, "", []string{t1.ID}, 1, 20)
	if err != nil || total != 1 || len(docFiltered) != 1 {
		t.Fatalf("filter by tag failed: total %d, docs %+v", total, docFiltered)
	}

	if err := store.DeleteTag(ctx, owner, t1.ID); err != nil {
		t.Fatal(err)
	}

	docAfterTagDelete, err := store.GetDoc(ctx, owner, docRes.Doc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(docAfterTagDelete.Tags) != 1 || docAfterTagDelete.Tags[0].ID != t2.ID {
		t.Fatalf("expected doc to retain remaining tag, got %+v", docAfterTagDelete.Tags)
	}
}
