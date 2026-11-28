package prompts

import "testing"

func TestRenderLeavesBracesInsideValues(t *testing.T) {
	got := Render("Q {query}\n{sources}", map[string]string{
		"query":   "数据集",
		"sources": "原文里写了 {query} 和 [[c1]]",
	})
	want := "Q 数据集\n原文里写了 {query} 和 [[c1]]"
	if got != want {
		t.Fatalf("got %q", got)
	}
}

func TestReadingTranslatePromptRendersSafely(t *testing.T) {
	if ReadingTranslate == "" {
		t.Fatal("ReadingTranslate prompt must not be empty")
	}
	got := Render(ReadingTranslate, map[string]string{
		"language":    "简体中文",
		"title":       "Paper {A}",
		"page":        "2",
		"source_text": "Equation {x} + {title}",
	})
	if got == ReadingTranslate {
		t.Fatal("expected placeholders to render")
	}
}