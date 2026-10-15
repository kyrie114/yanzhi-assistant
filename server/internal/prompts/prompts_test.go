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