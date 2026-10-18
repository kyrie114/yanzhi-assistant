package suggest

import (
	"errors"
	"strings"
	"testing"
)

func TestFingerprintIgnoresOrderAndUnready(t *testing.T) {
	ready := []SourceDoc{
		{ID: "b", Version: "1", Ready: true},
		{ID: "a", Version: "1", Ready: true},
	}
	reordered := []SourceDoc{
		{ID: "a", Version: "1", Ready: true},
		{ID: "b", Version: "1", Ready: true},
	}
	withDraft := append(append([]SourceDoc{}, reordered...), SourceDoc{ID: "c", Version: "9", Ready: false, Texts: []string{"草稿"}})
	if Fingerprint(ready) != Fingerprint(reordered) || Fingerprint(ready) != Fingerprint(withDraft) {
		t.Fatalf("fingerprint changed without a completed-document change")
	}
	changed := []SourceDoc{
		{ID: "a", Version: "2", Ready: true},
		{ID: "b", Version: "1", Ready: true},
	}
	if Fingerprint(ready) == Fingerprint(changed) {
		t.Fatal("version change kept the same fingerprint")
	}
}

type recordModel struct {
	calls [][]string
	fail  bool
}

func (m *recordModel) SuggestQuestions(texts []string) ([]string, error) {
	m.calls = append(m.calls, append([]string(nil), texts...))
	if m.fail {
		return nil, errors.New("model down")
	}
	return []string{"基于" + strings.Join(texts, "/")}, nil
}

func TestSetKBDocumentsGeneratesOnOpenAndSkipsUnchanged(t *testing.T) {
	m := NewMemory()
	model := &recordModel{}
	m.UseModel(model)
	m.PutKB("kb-1", "user-a", true, []string{"旧问题"})
	m.PutDocument("kept", "user-a", true, []string{"别动"})
	docs := []SourceDoc{
		{ID: "doc-1", Version: "v1", Ready: true, Texts: []string{"甲"}},
		{ID: "doc-2", Version: "v1", Ready: false, Texts: []string{"未完成"}},
	}
	m.SetKBDocuments("kb-1", "user-a", docs)
	if len(model.calls) != 1 || len(model.calls[0]) != 1 || model.calls[0][0] != "甲" {
		t.Fatalf("document generation %#v", model.calls)
	}
	doc, err := m.List("user-a", "document", "doc-1", "")
	if err != nil || doc.Status != "" || len(doc.Questions) != 1 || doc.Questions[0] != "基于甲" {
		t.Fatalf("document questions %v %#v", err, doc)
	}
	draft, err := m.List("user-a", "document", "doc-2", "")
	if err != nil || len(draft.Questions) != 0 {
		t.Fatalf("draft %#v", draft)
	}

	first, err := m.List("user-a", "kb", "kb-1", "")
	if err != nil || first.Status != StatusGenerating || len(first.Questions) != 1 || first.Questions[0] != "旧问题" {
		t.Fatalf("first open %v %#v", err, first)
	}
	if len(model.calls) != 2 || strings.Join(model.calls[1], "/") != "甲" {
		t.Fatalf("kb generation %#v", model.calls)
	}
	second, err := m.List("user-a", "kb", "kb-1", "")
	if err != nil || second.Status != StatusReady || len(second.Questions) != 1 || second.Questions[0] != "基于甲" {
		t.Fatalf("second open %v %#v", err, second)
	}

	m.SetKBDocuments("kb-1", "user-a", docs)
	again, err := m.List("user-a", "kb", "kb-1", "")
	if err != nil || again.Status != StatusReady || len(model.calls) != 2 {
		t.Fatalf("unchanged source regenerated %v calls %d %#v", err, len(model.calls), again)
	}

	kept, err := m.List("user-a", "document", "kept", "")
	if err != nil || len(kept.Questions) != 1 || kept.Questions[0] != "别动" {
		t.Fatalf("other document %#v", kept)
	}
}

func TestKBGenerationFailureClearsOnlyThatKB(t *testing.T) {
	m := NewMemory()
	model := &recordModel{}
	m.UseModel(model)
	m.PutKB("kb-2", "user-a", true, []string{"另一个库"})
	docs := []SourceDoc{{ID: "doc-1", Version: "v1", Ready: true, Texts: []string{"甲"}}}
	m.SetKBDocuments("kb-1", "user-a", docs)
	model.fail = true
	m.SetKBDocuments("kb-1", "user-a", []SourceDoc{{ID: "doc-1", Version: "v2", Ready: true, Texts: []string{"甲改"}}})
	failedDoc, err := m.List("user-a", "document", "doc-1", "")
	if err != nil || len(failedDoc.Questions) != 0 {
		t.Fatalf("failed document %#v", failedDoc)
	}
	first, err := m.List("user-a", "kb", "kb-1", "")
	if err != nil || first.Status != StatusGenerating {
		t.Fatalf("stale after failure %v %#v", err, first)
	}
	second, err := m.List("user-a", "kb", "kb-1", "")
	if err != nil || second.Status != StatusReady || len(second.Questions) != 0 {
		t.Fatalf("kb after failed refresh %v %#v", err, second)
	}
	other, err := m.List("user-a", "kb", "kb-2", "")
	if err != nil || len(other.Questions) != 1 || other.Questions[0] != "另一个库" {
		t.Fatalf("other kb %#v", other)
	}

	model.fail = false
	m.SetKBDocuments("kb-1", "user-a", []SourceDoc{{ID: "doc-1", Version: "v2", Ready: true, Texts: []string{"甲改"}}})
	if _, err := m.List("user-a", "document", "doc-1", ""); err != nil {
		t.Fatal(err)
	}
	// 同一版本失败后不再调用模型，直到版本再次变化。
	before := len(model.calls)
	m.SetKBDocuments("kb-1", "user-a", []SourceDoc{{ID: "doc-1", Version: "v2", Ready: true, Texts: []string{"甲改"}}})
	if len(model.calls) != before {
		t.Fatalf("retry on same version: calls %d want %d", len(model.calls), before)
	}
}

func TestSetKBDocumentsDoesNotStealAnotherUsersDocument(t *testing.T) {
	m := NewMemory()
	model := &recordModel{}
	m.UseModel(model)
	m.PutDocument("doc-1", "user-a", true, []string{"甲的问题"})
	m.SetKBDocuments("kb-b", "user-b", []SourceDoc{{ID: "doc-1", Version: "v1", Ready: true, Texts: []string{"偷"}}})
	got, err := m.List("user-a", "document", "doc-1", "")
	if err != nil || len(got.Questions) != 1 || got.Questions[0] != "甲的问题" {
		t.Fatalf("stolen %#v", got)
	}
	if len(model.calls) != 0 {
		t.Fatalf("generated for another user %#v", model.calls)
	}
	if _, err := m.List("user-a", "kb", "kb-b", ""); err != ErrNotFound {
		t.Fatal("owner changed")
	}
}
