package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"yanzhi/server/internal/apperr"
)

type Remote struct {
	Kind      string
	BaseURL   string
	Model     string
	APIKey    string
	Dimension int
}

type Client struct {
	HTTP *http.Client
}

func (c Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

type Usage struct {
	Prompt     int
	Completion int
}

func (c Client) Embed(ctx context.Context, remote Remote, texts []string) ([][]float32, Usage, error) {
	body, _ := json.Marshal(map[string]any{"model": remote.Model, "input": texts})
	var parsed struct {
		Data []struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
		Usage struct {
			PromptTokens int `json:"prompt_tokens"`
			TotalTokens  int `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := c.post(ctx, remote, "/embeddings", body, &parsed); err != nil {
		return nil, Usage{}, err
	}
	out := make([][]float32, len(texts))
	for _, row := range parsed.Data {
		if row.Index >= 0 && row.Index < len(out) {
			out[row.Index] = row.Embedding
		}
	}
	for _, vec := range out {
		if len(vec) == 0 {
			return nil, Usage{}, apperr.ErrModelDown
		}
	}
	return out, Usage{Prompt: parsed.Usage.PromptTokens}, nil
}

func (c Client) Complete(ctx context.Context, remote Remote, system, user string) (string, Usage, error) {
	body, _ := json.Marshal(map[string]any{
		"model": remote.Model,
		"messages": []map[string]string{
			{"role": "system", "content": system},
			{"role": "user", "content": user},
		},
	})
	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := c.post(ctx, remote, "/chat/completions", body, &parsed); err != nil {
		return "", Usage{}, err
	}
	if len(parsed.Choices) == 0 {
		return "", Usage{}, apperr.ErrModelDown
	}
	return parsed.Choices[0].Message.Content, Usage{Prompt: parsed.Usage.PromptTokens, Completion: parsed.Usage.CompletionTokens}, nil
}

func (c Client) Stream(ctx context.Context, remote Remote, system, user string, onDelta func(string) error) (Usage, error) {
	body, _ := json.Marshal(map[string]any{
		"model":  remote.Model,
		"stream": true,
		"messages": []map[string]string{
			{"role": "system", "content": system},
			{"role": "user", "content": user},
		},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint(remote.BaseURL, "/chat/completions"), bytes.NewReader(body))
	if err != nil {
		return Usage{}, apperr.ErrModelDown
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+remote.APIKey)
	resp, err := c.http().Do(req)
	if err != nil {
		return Usage{}, apperr.ErrModelDown
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return Usage{}, apperr.ErrModelDown
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Usage{}, apperr.ErrModelDown
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 2<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "[DONE]" {
			break
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if json.Unmarshal([]byte(payload), &chunk) != nil {
			continue
		}
		if len(chunk.Choices) == 0 || chunk.Choices[0].Delta.Content == "" {
			continue
		}
		if err := onDelta(chunk.Choices[0].Delta.Content); err != nil {
			return Usage{}, err
		}
	}
	if err := sc.Err(); err != nil && !errors.Is(err, context.Canceled) {
		return Usage{}, apperr.ErrModelDown
	}
	return Usage{}, nil
}

type Probe struct {
	Result    string
	Message   string
	ElapsedMS int
}

func (c Client) Probe(ctx context.Context, remote Remote) Probe {
	start := time.Now()
	var path string
	var body []byte
	switch remote.Kind {
	case "embedding":
		path = "/embeddings"
		body, _ = json.Marshal(map[string]any{"model": remote.Model, "input": "ping"})
	case "rerank":
		path = "/rerank"
		body, _ = json.Marshal(map[string]any{"model": remote.Model, "query": "ping", "documents": []string{"ping"}})
	default:
		path = "/chat/completions"
		body, _ = json.Marshal(map[string]any{
			"model": remote.Model, "max_tokens": 1,
			"messages": []map[string]string{{"role": "user", "content": "ping"}},
		})
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint(remote.BaseURL, path), bytes.NewReader(body))
	if err != nil {
		return Probe{Result: "unreachable", Message: "网络不可达", ElapsedMS: ms(start)}
	}
	req.Header.Set("Content-Type", "application/json")
	if remote.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+remote.APIKey)
	}
	resp, err := c.http().Do(req)
	elapsed := ms(start)
	if err != nil {
		return Probe{Result: "unreachable", Message: "网络不可达", ElapsedMS: elapsed}
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return Probe{Result: "unauthorized", Message: "鉴权失败", ElapsedMS: elapsed}
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return Probe{Result: "ok", Message: "连接成功", ElapsedMS: elapsed}
	default:
		return Probe{Result: "unreachable", Message: "网络不可达", ElapsedMS: elapsed}
	}
}

func (c Client) post(ctx context.Context, remote Remote, path string, body []byte, dest any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint(remote.BaseURL, path), bytes.NewReader(body))
	if err != nil {
		return apperr.ErrModelDown
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+remote.APIKey)
	resp, err := c.http().Do(req)
	if err != nil {
		var netErr net.Error
		if errors.As(err, &netErr) || errors.Is(err, context.DeadlineExceeded) {
			return apperr.ErrModelDown
		}
		return apperr.ErrModelDown
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return apperr.ErrModelDown
	}
	if err := json.Unmarshal(raw, dest); err != nil {
		return apperr.ErrModelDown
	}
	return nil
}

func endpoint(base, path string) string {
	return strings.TrimRight(base, "/") + path
}

func ms(start time.Time) int {
	return int(time.Since(start).Milliseconds())
}
