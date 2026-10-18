package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"yanzhi/server/internal/suggest"
)

func suggestAPI(m *suggest.Memory) http.Handler {
	return NewSuggestHandler(m, func(r *http.Request) (string, error) {
		if r.Header.Get("Authorization") == "" {
			return "", errors.New("missing token")
		}
		return r.Header.Get("X-User"), nil
	})
}

func getSuggest(t *testing.T, h http.Handler, path, user string) (int, suggest.Result) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if user != "" {
		req.Header.Set("Authorization", "Bearer token")
		req.Header.Set("X-User", user)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var got suggest.Result
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
	}
	return rec.Code, got
}

func TestSuggestedQuestionsQuickStartAndAfterAnswer(t *testing.T) {
	m := suggest.NewMemory()
	m.PutDocument("doc-1", "user-a", true, []string{"方法是什么？", "数据从哪来？"})
	m.PutDocument("doc-2", "user-a", false, []string{"还没完成的问题"})
	h := suggestAPI(m)

	status, got := getSuggest(t, h, "/api/v1/documents/doc-1/suggested-questions", "user-a")
	if status != http.StatusOK || got.Action != suggest.ActionFill || got.Status != "" || len(got.Questions) != 2 || got.Questions[0] != "方法是什么？" {
		t.Fatalf("quick start %d %#v", status, got)
	}

	asked := url.QueryEscape("方法是什么？")
	status, got = getSuggest(t, h, "/api/v1/documents/doc-1/suggested-questions?asked="+asked, "user-a")
	if status != http.StatusOK || got.Action != suggest.ActionFill || len(got.Questions) != 1 || got.Questions[0] != "数据从哪来？" {
		t.Fatalf("after answer %d %#v", status, got)
	}

	status, got = getSuggest(t, h, "/api/v1/documents/doc-2/suggested-questions", "user-a")
	if status != http.StatusOK || len(got.Questions) != 0 {
		t.Fatalf("not ready %d %#v", status, got)
	}

	status, _ = getSuggest(t, h, "/api/v1/documents/doc-1/suggested-questions", "user-b")
	if status != http.StatusNotFound {
		t.Fatalf("other user %d", status)
	}

	status, _ = getSuggest(t, h, "/api/v1/documents/doc-1/suggested-questions", "")
	if status != http.StatusUnauthorized {
		t.Fatalf("anonymous %d", status)
	}
}

func TestKBSuggestedQuestionsStayWhileGenerating(t *testing.T) {
	m := suggest.NewMemory()
	m.PutKB("kb-1", "user-a", true, []string{"旧问题"})
	m.SetKBSource("kb-1", "user-a", "fp-2")
	h := suggestAPI(m)

	status, got := getSuggest(t, h, "/api/v1/kbs/kb-1/suggested-questions", "user-a")
	if status != http.StatusOK || got.Status != suggest.StatusGenerating || got.Action != suggest.ActionFill || len(got.Questions) != 1 || got.Questions[0] != "旧问题" {
		t.Fatalf("generating %d %#v", status, got)
	}
	m.SaveKB("kb-1", "user-a", "fp-2", []string{"新问题"}, false)
	status, got = getSuggest(t, h, "/api/v1/kbs/kb-1/suggested-questions", "user-a")
	if status != http.StatusOK || got.Status != suggest.StatusReady || len(got.Questions) != 1 || got.Questions[0] != "新问题" {
		t.Fatalf("ready %d %#v", status, got)
	}
}
