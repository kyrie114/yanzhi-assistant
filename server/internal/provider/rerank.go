package provider

import (
	"context"
	"encoding/json"
	"sort"
)

// RerankResult 表示重排接口返回的单个文档索引及其相关度分数。
type RerankResult struct {
	Index          int     `json:"index"`
	RelevanceScore float64 `json:"relevance_score"`
}

// Rerank 调用已配置的 `/rerank` 接口对候选文档按相关度降序重排，保留同分时的原始稳定顺序。
func (c Client) Rerank(ctx context.Context, remote Remote, query string, docs []string) ([]RerankResult, Usage, error) {
	if len(docs) == 0 {
		return nil, Usage{}, nil
	}
	body, _ := json.Marshal(map[string]any{
		"model":     remote.Model,
		"query":     query,
		"documents": docs,
	})
	var parsed struct {
		Results []RerankResult `json:"results"`
		Usage   struct {
			PromptTokens int `json:"prompt_tokens"`
			TotalTokens  int `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := c.post(ctx, remote, "/rerank", body, &parsed); err != nil {
		return nil, Usage{}, err
	}

	seen := make(map[int]struct{}, len(parsed.Results))
	valid := make([]RerankResult, 0, len(parsed.Results))
	for _, r := range parsed.Results {
		if r.Index < 0 || r.Index >= len(docs) {
			continue
		}
		if _, dup := seen[r.Index]; dup {
			continue
		}
		seen[r.Index] = struct{}{}
		valid = append(valid, r)
	}
	sort.SliceStable(valid, func(i, j int) bool {
		if valid[i].RelevanceScore == valid[j].RelevanceScore {
			return valid[i].Index < valid[j].Index
		}
		return valid[i].RelevanceScore > valid[j].RelevanceScore
	})
	promptTokens := parsed.Usage.PromptTokens
	if promptTokens == 0 {
		promptTokens = parsed.Usage.TotalTokens
	}
	return valid, Usage{Prompt: promptTokens}, nil
}
