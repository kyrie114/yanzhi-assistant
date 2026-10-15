package retrieval

import "yanzhi/server/internal/textutil"

// 向量路在应用这一层丢掉低于阈值的命中。关键词路不读这个阈值。
func FilterThreshold(ids []string, scores []float64, threshold float64) []string {
	var out []string
	for i, id := range ids {
		if i < len(scores) && scores[i] >= threshold {
			out = append(out, id)
		}
	}
	return out
}

// Merge 在关键词路非空时保留关键词命中，即使向量路被阈值清空。
func Merge(vectorIDs, keywordIDs []string, rrfK, topK int) []string {
	fused := textutil.RRF([][]string{vectorIDs, keywordIDs}, rrfK)
	if topK > 0 && len(fused) > topK {
		return fused[:topK]
	}
	return fused
}

// VectorSQL 必须把所有者、已完成状态和代际留在 SQL 里。
const VectorSQL = `
SELECT c.id::text, 1 - (c.embedding <=> $1) AS similarity
FROM chunks c
JOIN documents d ON d.id = c.document_id
WHERE d.owner_id = $2
  AND d.status = 'ready'
  AND c.embedding_generation = $3
  AND c.embedding IS NOT NULL
  AND d.id = ANY($4::uuid[])
ORDER BY c.embedding <=> $1
LIMIT 20`

// KeywordSQL 不使用余弦距离。候选先限定在当前用户的已完成资料里。
const KeywordSQL = `
SELECT c.id::text, p.term, p.tf, c.term_count, ts.df
FROM chunk_postings p
JOIN chunks c ON c.id = p.chunk_id
JOIN documents d ON d.id = c.document_id
JOIN term_stats ts ON ts.owner_id = d.owner_id AND ts.term = p.term
WHERE d.owner_id = $1
  AND d.status = 'ready'
  AND d.id = ANY($2::uuid[])
  AND p.term = ANY($3::text[])`

const RecoverSQL = `
UPDATE documents
SET status = 'failed', failure_reason = '处理中断，请重试', updated_at = now()
WHERE status IN ('parsing', 'embedding');
UPDATE documents
SET graph_status = 'failed', graph_failure_reason = '处理中断，请重试', updated_at = now()
WHERE graph_status = 'extracting';
UPDATE jobs
SET status = 'failed', error_message = '处理中断，请重试', finished_at = now()
WHERE status = 'running';`
