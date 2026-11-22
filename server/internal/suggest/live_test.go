package suggest

import (
	"context"
	"errors"
	"testing"
)

type fakeCatalog struct {
	doc SourceDoc
	err error
}

func (f fakeCatalog) Document(context.Context, string, string) (SourceDoc, error) {
	return f.doc, f.err
}

func (f fakeCatalog) Library(context.Context, string, string) ([]SourceDoc, error) {
	if f.err != nil {
		return nil, f.err
	}
	return []SourceDoc{f.doc}, nil
}

type countModel struct {
	calls int
	out   []string
}

func (m *countModel) SuggestQuestions([]string) ([]string, error) {
	m.calls++
	return m.out, nil
}

func TestLiveDocumentGeneratesOnceAndSkipsUnfinished(t *testing.T) {
	mem := NewMemory()
	model := &countModel{out: []string{"这篇讲了什么？", "方法是什么？"}}
	mem.UseModel(model)
	live := Live{Memory: mem, Catalog: fakeCatalog{doc: SourceDoc{
		ID: "doc-1", Version: "v1", Ready: false,
	}}}
	got, err := live.List(context.Background(), "user-a", "document", "doc-1", "")
	if err != nil || len(got.Questions) != 0 || model.calls != 0 {
		t.Fatalf("unfinished questions %v calls %d err %v", got.Questions, model.calls, err)
	}

	live.Catalog = fakeCatalog{doc: SourceDoc{
		ID: "doc-1", Version: "v1", Ready: true, Texts: []string{"LoRA 降低了训练成本"},
	}}
	got, err = live.List(context.Background(), "user-a", "document", "doc-1", "")
	if err != nil || len(got.Questions) != 2 || got.Action != ActionFill || model.calls != 1 {
		t.Fatalf("first list %+v calls %d err %v", got, model.calls, err)
	}
	got, err = live.List(context.Background(), "user-a", "document", "doc-1", "这篇讲了什么？")
	if err != nil || len(got.Questions) != 1 || got.Questions[0] != "方法是什么？" || model.calls != 1 {
		t.Fatalf("second list %+v calls %d err %v", got, model.calls, err)
	}
}

func TestLiveMissingDocumentIsNotFound(t *testing.T) {
	live := Live{Memory: NewMemory(), Catalog: fakeCatalog{err: ErrNotFound}}
	_, err := live.List(context.Background(), "user-a", "document", "missing", "")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err %v", err)
	}
}
