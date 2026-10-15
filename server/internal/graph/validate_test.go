package graph

import "testing"

func TestMergeKeyAndEvidence(t *testing.T) {
	if NormalizeKey("  LoRA  ") != "lora" {
		t.Fatal(NormalizeKey("  LoRA  "))
	}
	if PaperKey("doc-1") == PaperKey("doc-2") {
		t.Fatal("paper keys must include document id")
	}
	chunks := []ChunkView{{Alias: "c1", ID: "ch", Page: 6, Text: "作者 Zhang 提出方法 LoRA。"}}
	ents := []RawEntity{{Type: "method", Name: "LoRA", Alias: "c1"}, {Type: "method", Name: "系统提示词", Alias: "c1"}}
	rels := []RawRelation{{
		Type: "proposes", Source: "PAPER", Target: "LoRA", Alias: "c1", Sentence: "提出方法 LoRA",
	}}
	gotE, gotR := ValidateExtraction(chunks, ents, rels)
	if len(gotE) != 1 || gotE[0].Name != "LoRA" {
		t.Fatalf("entities %+v", gotE)
	}
	if len(gotR) != 1 || gotR[0].Page != 6 {
		t.Fatalf("relations %+v", gotR)
	}
}
