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

func TestGraphUnitHelpers(t *testing.T) {
	v := emptyView()
	if len(v.Nodes) != 0 || len(v.Edges) != 0 {
		t.Fatalf("emptyView not empty: %+v", v)
	}
	if statusLabel("") != nil {
		t.Fatal("expected nil statusLabel for empty code")
	}
	if got := statusLabel("ready"); got == nil || *got != "已完成" {
		t.Fatalf("statusLabel(ready) = %v", got)
	}
	if (Service{}).timeout().Seconds() != 60 {
		t.Fatal("expected default 60s timeout")
	}
	if itoa(0) != "0" || itoa(42) != "42" {
		t.Fatal("itoa mismatch")
	}
	if nullUUID("") != nil || nullUUID("not-a-uuid") != nil {
		t.Fatal("expected nil for invalid uuid")
	}
	if len(uuidSlice([]string{"not-a-uuid"})) != 0 {
		t.Fatal("expected empty uuidSlice for invalid input")
	}
	ents, rels, err := decodeExtraction("```json\n{\"entities\":[{\"type\":\"method\",\"name\":\"LoRA\",\"alias\":\"c1\"}],\"relations\":[]}\n```")
	if err != nil || len(ents) != 1 || len(rels) != 0 {
		t.Fatalf("decodeExtraction fenced failed: %v %v %v", ents, rels, err)
	}
	if _, _, err := decodeExtraction("not-json"); err == nil {
		t.Fatal("expected decodeExtraction error on invalid json")
	}
}
