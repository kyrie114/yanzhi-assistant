package ingest

import (
	"strings"
	"testing"

	"yanzhi/server/internal/textutil"
)

func TestSkippedPageDoesNotRenumber(t *testing.T) {
	pages := []Page{
		{PageNo: 5, Blocks: []Block{{Kind: "paragraph", Text: strings.Repeat("甲", 200)}}},
	}
	chunks, _ := Pack(pages, DefaultConfig())
	if len(chunks) != 1 || chunks[0].PageStart != 5 {
		t.Fatalf("%+v", chunks)
	}
}

func TestOverlapUsesNewPage(t *testing.T) {
	cfg := DefaultConfig()
	pages := []Page{
		{PageNo: 1, Blocks: []Block{{Kind: "paragraph", Text: strings.Repeat("a", 800)}}},
		{PageNo: 2, Blocks: []Block{{Kind: "paragraph", Text: strings.Repeat("b", 800)}}},
	}
	chunks, _ := Pack(pages, cfg)
	if len(chunks) < 2 {
		t.Fatalf("got %d", len(chunks))
	}
	if chunks[0].PageStart != 1 || chunks[0].OverlapPrefix != 0 {
		t.Fatalf("first %+v", chunks[0])
	}
	if chunks[1].OverlapPrefix != 100 || chunks[1].PageStart != 2 {
		t.Fatalf("second page=%d overlap=%d text=%d", chunks[1].PageStart, chunks[1].OverlapPrefix, textutil.RuneCount(chunks[1].Text))
	}
	if textutil.RuneCount(chunks[1].Text) > cfg.Max {
		t.Fatalf("chunk exceeded max: %d", textutil.RuneCount(chunks[1].Text))
	}
}

func TestParentIsFreshConcat(t *testing.T) {
	cfg := Config{Min: 10, Max: 20, Overlap: 4, ParentMin: 30, ParentMax: 40}
	pages := []Page{{PageNo: 1, Blocks: []Block{{Kind: "paragraph", Text: strings.Repeat("文", 80)}}}}
	chunks, parents := Pack(pages, cfg)
	if len(parents) == 0 || len(chunks) == 0 {
		t.Fatal("empty")
	}
	fresh := ""
	for _, ch := range chunks {
		if ch.ParentIndex != 0 && parents[0].Text != "" && ch.ParentIndex == 0 {
		}
		if ch.ParentIndex == 0 {
			fresh += textutil.DropRunes(ch.Text, ch.OverlapPrefix)
		}
	}
	if parents[0].Text != fresh {
		t.Fatalf("parent mismatch\n%q\n%q", parents[0].Text, fresh)
	}
}
