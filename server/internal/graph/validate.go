package graph

import (
	"strings"
	"unicode"
)

var entityTypes = map[string]struct{}{
	"paper": {}, "author": {}, "method": {}, "dataset": {}, "metric": {}, "problem": {},
}

var relationEnds = map[string][2]string{
	"author_of":      {"author", "paper"},
	"proposes":       {"paper", "method"},
	"uses_dataset":   {"paper", "dataset"},
	"reports_metric": {"paper", "metric"},
	"addresses":      {"paper", "problem"},
	"compares_with":  {"method", "method"},
	"cites":          {"paper", "paper"},
}

type ChunkView struct {
	Alias string
	Text  string
	ID    string
	Page  int
}

type RawEntity struct {
	Type  string `json:"type"`
	Name  string `json:"name"`
	Alias string `json:"chunk_alias"`
}

type RawRelation struct {
	Type     string `json:"type"`
	Source   string `json:"source"`
	Target   string `json:"target"`
	Alias    string `json:"chunk_alias"`
	Sentence string `json:"sentence"`
}

type AcceptedEntity struct {
	Type  string
	Name  string
	Alias string
}

type AcceptedRelation struct {
	Type     string
	Source   string
	Target   string
	Alias    string
	Sentence string
	ChunkID  string
	Page     int
	Cites    bool
}

func NormalizeKey(name string) string {
	s := strings.Join(strings.Fields(name), " ")
	var b strings.Builder
	for _, r := range s {
		if r <= 127 {
			b.WriteRune(unicode.ToLower(r))
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func PaperKey(documentID string) string { return "paper:" + documentID }

func ValidateExtraction(chunks []ChunkView, ents []RawEntity, rels []RawRelation) ([]AcceptedEntity, []AcceptedRelation) {
	byAlias := map[string]ChunkView{}
	for _, c := range chunks {
		byAlias[c.Alias] = c
	}
	var accepted []AcceptedEntity
	names := map[string]string{}
	for _, e := range ents {
		if _, ok := entityTypes[e.Type]; !ok || e.Type == "paper" {
			continue
		}
		ch, ok := byAlias[e.Alias]
		if !ok || strings.TrimSpace(e.Name) == "" {
			continue
		}
		if !strings.Contains(ch.Text, strings.TrimSpace(e.Name)) {
			continue
		}
		name := strings.TrimSpace(e.Name)
		accepted = append(accepted, AcceptedEntity{Type: e.Type, Name: name, Alias: e.Alias})
		names[name] = e.Type
		names[NormalizeKey(name)] = e.Type
	}
	names["PAPER"] = "paper"
	var out []AcceptedRelation
	for _, r := range rels {
		ends, ok := relationEnds[r.Type]
		if !ok {
			continue
		}
		ch, ok := byAlias[r.Alias]
		if !ok || strings.TrimSpace(r.Sentence) == "" {
			continue
		}
		if !strings.Contains(ch.Text, r.Sentence) {
			continue
		}
		srcType, srcOK := names[r.Source]
		if !srcOK {
			srcType, srcOK = names[NormalizeKey(r.Source)]
		}
		if r.Source == "PAPER" {
			srcType, srcOK = "paper", true
		}
		tgtType, tgtOK := names[r.Target]
		if !tgtOK {
			tgtType, tgtOK = names[NormalizeKey(r.Target)]
		}
		if r.Target == "PAPER" {
			tgtType, tgtOK = "paper", true
		}
		citesTitle := false
		if r.Type == "cites" && !tgtOK {
			tgtType, tgtOK = "paper", true
			citesTitle = true
		}
		if !srcOK || !tgtOK || srcType != ends[0] || tgtType != ends[1] {
			continue
		}
		if !citesTitle && r.Source != "PAPER" && !strings.Contains(ch.Text, strings.TrimSpace(r.Source)) {
			continue
		}
		if !citesTitle && r.Target != "PAPER" && !strings.Contains(ch.Text, strings.TrimSpace(r.Target)) {
			continue
		}
		out = append(out, AcceptedRelation{
			Type: r.Type, Source: r.Source, Target: r.Target, Alias: r.Alias,
			Sentence: r.Sentence, ChunkID: ch.ID, Page: ch.Page, Cites: citesTitle,
		})
	}
	return accepted, out
}
