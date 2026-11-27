package suggest

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"yanzhi/server/internal/platform/db"
	"yanzhi/server/internal/prompts"
	"yanzhi/server/internal/provider"
)

const suggestPrompt = "摘录：\n{chunks}\n\n根据摘录写出不超过 5 个读者可能会问的简短问题。只输出 JSON 字符串数组。"

type ChatModel struct {
	Models  provider.Store
	AI      provider.Client
	Pool    *pgxpool.Pool
	Logger  *slog.Logger
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
	rendered := prompts.Render(suggestPrompt, map[string]string{"chunks": joined})
	start := time.Now()
	body, usage, err := c.AI.Complete(ctx, remote, rendered, "只输出 JSON 数组")
	elapsed := int(time.Since(start).Milliseconds())
	status := "ok"
	if err != nil {
		status = "error"
	}
	provider.RecordModelCall(ctx, c.Pool, c.Logger, provider.CallMeta{
		RequestID:  db.NewID(),
		Kind:       "suggest",
		Model:      remote.Model,
		ElapsedMS:  elapsed,
		Usage:      usage,
		Status:     status,
		PromptHash: provider.PromptSHA256(rendered),
	})
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
