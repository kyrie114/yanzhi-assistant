package ingest

import (
	"yanzhi/server/internal/platform/docreader"
	"yanzhi/server/internal/textutil"
)

// Page 和 Block 的字段与 docreader 交回的一致，这里用别名避免两边各写一份。
type Block = docreader.Block

type Page = docreader.Page

type ChunkDraft struct {
	Text           string
	PageStart      int
	ParagraphIndex int
	OverlapPrefix  int
	ParentIndex    int
	Kind           string
}

type ParentDraft struct {
	Position  int
	PageStart int
	Text      string
}

type Config struct {
	Min, Max, Overlap int
	ParentMin         int
	ParentMax         int
}

func DefaultConfig() Config {
	return Config{Min: 500, Max: 800, Overlap: 100, ParentMin: 2000, ParentMax: 3000}
}

type piece struct {
	text    string
	page    int
	kind    string
	overlap bool
}

// Pack 把页块收成子块和父块。箱的上限含重叠前缀，单段按剩余空间切开，避免重叠把一块撑过 Max。
func Pack(pages []Page, cfg Config) ([]ChunkDraft, []ParentDraft) {
	if cfg.Max <= 0 {
		cfg = DefaultConfig()
	}
	var chunks []ChunkDraft
	var box []piece
	flush := func() {
		if !hasNew(box) {
			box = nil
			return
		}
		text := join(box)
		op := overlapRunes(box)
		page := firstNewPage(box)
		kind := "text"
		if allNewKind(box, "table") {
			kind = "table"
		}
		chunks = append(chunks, ChunkDraft{
			Text:           text,
			PageStart:      page,
			ParagraphIndex: len(chunks) + 1,
			OverlapPrefix:  op,
			Kind:           kind,
		})
		tailN := cfg.Overlap
		if tailN > textutil.RuneCount(text) {
			tailN = textutil.RuneCount(text)
		}
		tail := textutil.LastRunes(text, tailN)
		if tail == "" {
			box = nil
			return
		}
		box = []piece{{text: tail, overlap: true}}
	}
	add := func(text string, page int, kind string) {
		rest := text
		for rest != "" {
			room := cfg.Max - boxRunes(box)
			if room <= 0 {
				flush()
				continue
			}
			n := textutil.RuneCount(rest)
			if n <= room {
				box = append(box, piece{text: rest, page: page, kind: kind})
				rest = ""
				if boxRunes(box) >= cfg.Max {
					flush()
				}
				return
			}
			if hasNew(box) {
				flush()
				continue
			}
			cut := textutil.TakeRunes(rest, room)
			box = append(box, piece{text: cut, page: page, kind: kind})
			rest = textutil.DropRunes(rest, room)
			flush()
		}
	}
	for _, p := range pages {
		for _, bl := range p.Blocks {
			if textutil.RuneCount(trimSpace(bl.Text)) == 0 {
				continue
			}
			add(bl.Text, p.PageNo, bl.Kind)
		}
	}
	if hasNew(box) {
		text := join(box)
		chunks = append(chunks, ChunkDraft{
			Text:           text,
			PageStart:      firstNewPage(box),
			ParagraphIndex: len(chunks) + 1,
			OverlapPrefix:  overlapRunes(box),
			Kind:           kindOf(box),
		})
	}
	parents := packParents(chunks, cfg)
	return chunks, parents
}

func packParents(chunks []ChunkDraft, cfg Config) []ParentDraft {
	var parents []ParentDraft
	var buf []string
	var length int
	var page int
	var childFrom int
	close := func(upto int) {
		if len(buf) == 0 {
			return
		}
		text := ""
		for _, part := range buf {
			text += part
		}
		parents = append(parents, ParentDraft{
			Position:  len(parents) + 1,
			PageStart: page,
			Text:      text,
		})
		for i := childFrom; i < upto; i++ {
			chunks[i].ParentIndex = len(parents) - 1
		}
		buf = nil
		length = 0
	}
	for i, ch := range chunks {
		fresh := textutil.DropRunes(ch.Text, ch.OverlapPrefix)
		n := textutil.RuneCount(fresh)
		if len(buf) > 0 && (length+n > cfg.ParentMax || length >= cfg.ParentMin) {
			close(i)
		}
		if len(buf) == 0 {
			page = ch.PageStart
			childFrom = i
		}
		buf = append(buf, fresh)
		length += n
		if length >= cfg.ParentMin {
			close(i + 1)
		}
	}
	close(len(chunks))
	return parents
}

func boxRunes(b []piece) int {
	n := 0
	for _, p := range b {
		n += textutil.RuneCount(p.text)
	}
	return n
}

func hasNew(b []piece) bool {
	for _, p := range b {
		if !p.overlap && p.text != "" {
			return true
		}
	}
	return false
}

func join(b []piece) string {
	out := ""
	for _, p := range b {
		out += p.text
	}
	return out
}

func overlapRunes(b []piece) int {
	n := 0
	for _, p := range b {
		if !p.overlap {
			break
		}
		n += textutil.RuneCount(p.text)
	}
	return n
}

func firstNewPage(b []piece) int {
	for _, p := range b {
		if !p.overlap {
			return p.page
		}
	}
	return 1
}

func allNewKind(b []piece, kind string) bool {
	any := false
	for _, p := range b {
		if p.overlap {
			continue
		}
		any = true
		if p.kind != kind {
			return false
		}
	}
	return any
}

func kindOf(b []piece) string {
	if allNewKind(b, "table") {
		return "table"
	}
	return "text"
}

func trimSpace(s string) string {
	return stringsTrim(s)
}

func stringsTrim(s string) string {
	rs := []rune(s)
	i, j := 0, len(rs)
	for i < j && isSpace(rs[i]) {
		i++
	}
	for j > i && isSpace(rs[j-1]) {
		j--
	}
	return string(rs[i:j])
}

func isSpace(r rune) bool {
	return r == ' ' || r == '\n' || r == '\t' || r == '\r'
}
