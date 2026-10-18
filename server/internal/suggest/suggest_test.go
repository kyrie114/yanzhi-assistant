package suggest

import (
	"errors"
	"testing"
)

func TestQuickStartCapsAtFive(t *testing.T) {
	m := NewMemory()
	m.PutDocument("doc-1", "user-a", true, []string{
		"方法是什么？", " 方法是什么？ ", "", "数据从哪来？", "局限是什么？", "和前文的差别？", "还需要什么实验？", "第六个不该出现",
	})
	got, err := m.List("user-a", "document", "doc-1", "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Action != ActionFill {
		t.Fatalf("action %s", got.Action)
	}
	want := []string{"方法是什么？", "数据从哪来？", "局限是什么？", "和前文的差别？", "还需要什么实验？"}
	if len(got.Questions) != len(want) {
		t.Fatalf("questions %#v", got.Questions)
	}
	for i := range want {
		if got.Questions[i] != want[i] {
			t.Fatalf("questions %#v", got.Questions)
		}
	}
}

func TestNotReadyIsEmpty(t *testing.T) {
	m := NewMemory()
	m.PutDocument("doc-1", "user-a", false, []string{"方法是什么？"})
	got, err := m.List("user-a", "document", "doc-1", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Questions) != 0 || got.Action != ActionFill {
		t.Fatalf("%#v", got)
	}
}

func TestOtherUserIsNotFound(t *testing.T) {
	m := NewMemory()
	m.PutKB("kb-1", "user-a", true, []string{"这个库在比较什么？"})
	if _, err := m.List("user-b", "kb", "kb-1", ""); err != ErrNotFound {
		t.Fatalf("other user: %v", err)
	}
	if _, err := m.List("user-a", "kb", "missing", ""); err != ErrNotFound {
		t.Fatalf("missing: %v", err)
	}
}

func TestAfterAnswerDropsAskedQuestion(t *testing.T) {
	m := NewMemory()
	m.PutDocument("doc-1", "user-a", true, []string{
		"方法是什么？", "数据从哪来？", "局限是什么？", "和前文的差别？", "还需要什么实验？", "下一问是什么？",
	})
	got, err := m.List("user-a", "document", "doc-1", " 方法是什么？ ")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Questions) != MaxQuestions || got.Questions[0] != "数据从哪来？" || got.Questions[4] != "下一问是什么？" {
		t.Fatalf("%#v", got.Questions)
	}
	for _, q := range got.Questions {
		if q == "方法是什么？" {
			t.Fatal("asked question was recommended again")
		}
	}
}

type scriptModel struct {
	texts     []string
	questions []string
	err       error
	called    bool
}

func (m *scriptModel) SuggestQuestions(texts []string) ([]string, error) {
	m.called = true
	m.texts = append([]string(nil), texts...)
	return m.questions, m.err
}

func TestGenerateDocumentStoresFiveAndSkipsUnready(t *testing.T) {
	m := NewMemory()
	model := &scriptModel{questions: []string{" 方法是什么？ ", "方法是什么？", "", "数据从哪来？", "局限是什么？", "和前文的差别？", "还需要什么实验？", "第六个不该留下"}}
	m.GenerateDocument("doc-1", "user-a", true, []string{"第一段", "第二段"}, model)
	if !model.called || len(model.texts) != 2 || model.texts[0] != "第一段" {
		t.Fatalf("model input %#v called %v", model.texts, model.called)
	}
	got, err := m.List("user-a", "document", "doc-1", "")
	if err != nil || got.Status != "" || len(got.Questions) != MaxQuestions || got.Questions[0] != "方法是什么？" || got.Questions[4] != "还需要什么实验？" {
		t.Fatalf("%v %#v", err, got)
	}

	skipped := &scriptModel{questions: []string{"不该出现"}}
	m.GenerateDocument("doc-2", "user-a", false, []string{"未完成"}, skipped)
	if skipped.called {
		t.Fatal("generator ran for an unfinished document")
	}
	got, err = m.List("user-a", "document", "doc-2", "")
	if err != nil || len(got.Questions) != 0 {
		t.Fatalf("unready %#v", got)
	}
}

func TestGenerateDocumentFailureLeavesOtherDocument(t *testing.T) {
	m := NewMemory()
	m.PutDocument("other", "user-a", true, []string{"还在"})
	m.GenerateDocument("doc-1", "user-a", true, []string{"正文"}, &scriptModel{err: errors.New("model down")})
	got, err := m.List("user-a", "document", "doc-1", "")
	if err != nil || len(got.Questions) != 0 {
		t.Fatalf("failed generation %#v", got)
	}
	got, err = m.List("user-a", "document", "other", "")
	if err != nil || len(got.Questions) != 1 || got.Questions[0] != "还在" {
		t.Fatalf("other document %#v", got)
	}
}

func TestKBRefreshKeepsCacheUntilSave(t *testing.T) {
	m := NewMemory()
	m.PutKB("kb-1", "user-a", true, []string{"旧问题", "另一个"})
	m.PutKB("kb-2", "user-b", true, []string{"别人的库"})
	var fires []string
	m.OnKBStale(func(id, owner, fingerprint string) {
		fires = append(fires, id+"|"+owner+"|"+fingerprint)
	})
	m.SetKBSource("kb-1", "user-b", "stolen")
	m.SetKBSource("kb-1", "user-a", "fp-2")

	got, err := m.List("user-a", "kb", "kb-1", "")
	if err != nil || got.Status != StatusGenerating || len(got.Questions) != 2 || got.Questions[0] != "旧问题" {
		t.Fatalf("stale open %v %#v", err, got)
	}
	if len(fires) != 1 || fires[0] != "kb-1|user-a|fp-2" {
		t.Fatalf("fires %#v", fires)
	}
	got, err = m.List("user-a", "kb", "kb-1", "旧问题")
	if err != nil || got.Status != StatusGenerating || len(got.Questions) != 1 || got.Questions[0] != "另一个" || len(fires) != 1 {
		t.Fatalf("second open %v %#v fires %#v", err, got, fires)
	}

	m.SaveKB("kb-1", "user-a", "fp-1", []string{"过期结果"}, false)
	m.SaveKB("kb-1", "user-a", "fp-2", []string{" 新问题 ", "新问题", "", "下一问"}, false)
	got, err = m.List("user-a", "kb", "kb-1", "")
	if err != nil || got.Status != StatusReady || len(got.Questions) != 2 || got.Questions[0] != "新问题" || len(fires) != 1 {
		t.Fatalf("saved %v %#v fires %#v", err, got, fires)
	}

	m.SetKBSource("kb-1", "user-a", "fp-3")
	m.SaveKB("kb-1", "user-a", "fp-3", []string{"不该出现"}, true)
	got, err = m.List("user-a", "kb", "kb-1", "")
	if err != nil || got.Status != StatusReady || len(got.Questions) != 0 {
		t.Fatalf("failed refresh %v %#v", err, got)
	}
	if _, err := m.List("user-b", "kb", "kb-1", ""); err != ErrNotFound {
		t.Fatal("other user saw the kb")
	}
	other, err := m.List("user-b", "kb", "kb-2", "")
	if err != nil || other.Status != StatusReady || len(other.Questions) != 1 || other.Questions[0] != "别人的库" {
		t.Fatalf("other kb %#v", other)
	}
}
