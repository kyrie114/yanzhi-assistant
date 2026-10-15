package textutil

import (
	"math"
	"sort"
	"strings"
	"unicode"
)

// Terms 按设计分词：拉丁词保留连字符并转小写，连续汉字按二元组。
func Terms(s string) []string {
	rs := []rune(s)
	var out []string
	for i := 0; i < len(rs); {
		if isAlnum(rs[i]) {
			j := scanLatin(rs, i)
			out = append(out, strings.ToLower(string(rs[i:j])))
			i = j
			continue
		}
		if unicode.Is(unicode.Han, rs[i]) {
			j := i
			for j < len(rs) && unicode.Is(unicode.Han, rs[j]) {
				j++
			}
			run := rs[i:j]
			if len(run) == 1 {
				out = append(out, string(run))
			} else {
				for k := 0; k < len(run)-1; k++ {
					out = append(out, string(run[k:k+2]))
				}
			}
			i = j
			continue
		}
		i++
	}
	return out
}

func scanLatin(rs []rune, i int) int {
	j := i + 1
	for j < len(rs) && isAlnum(rs[j]) {
		j++
	}
	for j < len(rs) && rs[j] == '-' && j+1 < len(rs) && isAlnum(rs[j+1]) {
		j++
		for j < len(rs) && isAlnum(rs[j]) {
			j++
		}
	}
	return j
}

func isAlnum(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
}

func BM25(tf, df, docLen, avgLen, nDocs, k1, b float64) float64 {
	if df <= 0 || nDocs <= 0 {
		return 0
	}
	idf := math.Log(1 + (nDocs-df+0.5)/(df+0.5))
	norm := 1 - b + b*(docLen/math.Max(avgLen, 1))
	return idf * (tf * (k1 + 1)) / (tf + k1*norm)
}

type Hit struct {
	ID    string
	Score float64
	Rank  int
}

// RRF 按名次融合。k 是常数，rank 从 1 开始。
func RRF(lists [][]string, k int) []string {
	if k <= 0 {
		k = 60
	}
	type acc struct {
		score float64
		first int
	}
	scores := map[string]*acc{}
	order := 0
	for _, list := range lists {
		for i, id := range list {
			a, ok := scores[id]
			if !ok {
				a = &acc{first: order}
				scores[id] = a
				order++
			}
			a.score += 1 / float64(k+i+1)
		}
	}
	ids := make([]string, 0, len(scores))
	for id := range scores {
		ids = append(ids, id)
	}
	sort.SliceStable(ids, func(i, j int) bool {
		si, sj := scores[ids[i]], scores[ids[j]]
		if si.score == sj.score {
			return si.first < sj.first
		}
		return si.score > sj.score
	})
	return ids
}

func RuneCount(s string) int { return len([]rune(s)) }

func TakeRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	rs := []rune(s)
	if n >= len(rs) {
		return s
	}
	return string(rs[:n])
}

func DropRunes(s string, n int) string {
	if n <= 0 {
		return s
	}
	rs := []rune(s)
	if n >= len(rs) {
		return ""
	}
	return string(rs[n:])
}

func LastRunes(s string, n int) string {
	rs := []rune(s)
	if n >= len(rs) {
		return s
	}
	if n <= 0 {
		return ""
	}
	return string(rs[len(rs)-n:])
}
