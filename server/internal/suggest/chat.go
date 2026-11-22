package suggest

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"yanzhi/server/internal/prompts"
	"yanzhi/server/internal/provider"
)

const suggestPrompt = "摘录：\n{chunks}\n\n根据摘录写出不超过 5 个读者可能会问的简短问题。只输出 JSON 字符串数组。"

type ChatModel struct {
	Models  provider.Store
	AI      provider.Client
	Timeout time.Duration
}

func (c ChatModel) SuggestQuestions(texts []string) ([]string, error) {
	joined := strings.TrimSpace(strings.Join(texts, "\n"))
	if joined == "" {
		return nil, errEmptySource
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	remote, err := c.Models.Load(ctx, "chat")
	if err != nil {
		return nil, err
	}
	body, _, err := c.AI.Complete(ctx, remote, prompts.Render(suggestPrompt, map[string]string{"chunks": joined}), "只输出 JSON 数组")
	if err != nil {
		return nil, err
	}
	return parseQuestions(body)
}

var errEmptySource = errString("empty source")

type errString string

func (e errString) Error() string { return string(e) }

func parseQuestions(body string) ([]string, error) {
	body = strings.TrimSpace(body)
	body = strings.TrimPrefix(body, "```json")
	body = strings.TrimPrefix(body, "```")
	body = strings.TrimSuffix(body, "```")
	body = strings.TrimSpace(body)
	var questions []string
	if err := json.Unmarshal([]byte(body), &questions); err != nil {
		return nil, err
	}
	return questions, nil
}
