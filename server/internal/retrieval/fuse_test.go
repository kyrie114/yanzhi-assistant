package retrieval

import (
	"strings"
	"testing"
)

func TestKeywordSurvivesLowVectorScore(t *testing.T) {
	vec := FilterThreshold([]string{"other"}, []float64{0.1}, 0.3)
	merged := Merge(vec, []string{"zephyr"}, 60, 5)
	if len(merged) != 1 || merged[0] != "zephyr" {
		t.Fatalf("%v", merged)
	}
}

func TestSQLPredicates(t *testing.T) {
	if !strings.Contains(VectorSQL, "d.owner_id") || !strings.Contains(VectorSQL, "d.status = 'ready'") || !strings.Contains(VectorSQL, "embedding_generation") {
		t.Fatal("vector sql missing owner filter")
	}
	if strings.Contains(KeywordSQL, "<=>") {
		t.Fatal("keyword sql must not apply cosine")
	}
	if !strings.Contains(KeywordSQL, "d.owner_id") || !strings.Contains(KeywordSQL, "d.status = 'ready'") {
		t.Fatal("keyword sql missing owner filter")
	}
	if strings.Contains(RecoverSQL, "manual_retry_count") || !strings.Contains(RecoverSQL, "处理中断，请重试") {
		t.Fatal("recovery must not touch manual retries")
	}
}
