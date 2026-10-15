package qa

import "testing"

func TestDecoderDropsUnknownAndSplitsMarker(t *testing.T) {
	d := NewDecoder([]Alias{{Name: "c1", Title: "论文", Page: 4, ChunkID: "chunk"}})
	a := d.Push("数据集 [[c")
	b := d.Push("1]] 和 [[c9]]")
	c := d.Flush()
	visible := a + b + c
	if stringsContains(visible, "[[") {
		t.Fatalf("marker leaked: %q", visible)
	}
	used := d.Used()
	if len(used) != 1 || used[0].Name != "c1" {
		t.Fatalf("%+v", used)
	}
	answer, cites, kind := Finalize(visible, used, false)
	if kind != "kb" || len(cites) != 1 || Label(cites[0].Title, cites[0].Page) != "论文, 第 4 页" {
		t.Fatalf("%s %s %+v", answer, kind, cites)
	}
}

func TestRefuseWithoutAlias(t *testing.T) {
	answer, cites, kind := Finalize("随便写", nil, false)
	if answer != Refusal || cites != nil || kind != "refused" {
		t.Fatalf("%s %s", answer, kind)
	}
}

func TestSmalltalk(t *testing.T) {
	if !IsSmalltalk("你好") || IsSmalltalk("这篇论文用了什么数据集") {
		t.Fatal("smalltalk rule")
	}
}

func stringsContains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
