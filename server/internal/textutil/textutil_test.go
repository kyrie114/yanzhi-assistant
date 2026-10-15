package textutil

import "testing"

func TestTermsKeepsHyphenatedName(t *testing.T) {
	got := Terms("Zephyr-Bench-9 是什么")
	if len(got) == 0 || got[0] != "zephyr-bench-9" {
		t.Fatalf("%q", got)
	}
	found := false
	for _, term := range got {
		if term == "是什" || term == "什么" {
			found = true
		}
	}
	if !found {
		t.Fatalf("cjk bigrams missing: %q", got)
	}
}

func TestRRFKeepsKeywordWhenVectorEmpty(t *testing.T) {
	fused := RRF([][]string{nil, {"chunk-a"}}, 60)
	if len(fused) != 1 || fused[0] != "chunk-a" {
		t.Fatalf("%v", fused)
	}
}
