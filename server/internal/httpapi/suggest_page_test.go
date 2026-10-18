package httpapi

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"yanzhi/server/internal/suggest"
)

func assistantReq(t *testing.T, h http.Handler, method, path, user string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req := httptest.NewRequest(method, path, body)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if user != "" {
		req.Header.Set("Authorization", "Bearer token")
		req.Header.Set("X-User", user)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestAssistantPageFillsThenSubmits(t *testing.T) {
	m := suggest.NewMemory()
	m.PutDocument("doc-1", "user-a", true, []string{"方法是什么？", "数据从哪来？"})
	m.PutDocument("doc-2", "user-a", false, []string{"还没完成的问题"})
	m.PutKB("kb-1", "user-a", true, []string{"这个库在讲什么？"})
	h := suggestAPI(m)

	quick := assistantReq(t, h, http.MethodGet, "/assistant?scope=document&id=doc-1", "user-a", nil)
	if quick.Code != http.StatusOK || !strings.Contains(quick.Body.String(), "<h2>快速开始</h2>") {
		t.Fatalf("quick start %d %s", quick.Code, quick.Body.String())
	}
	if !strings.Contains(quick.Body.String(), ">方法是什么？</a>") || !strings.Contains(quick.Body.String(), ">数据从哪来？</a>") {
		t.Fatalf("missing questions %s", quick.Body.String())
	}
	if !strings.Contains(quick.Body.String(), `<textarea name="question"></textarea>`) || strings.Contains(quick.Body.String(), "上一问") {
		t.Fatalf("draft should be empty %s", quick.Body.String())
	}

	fillQuery := url.Values{}
	fillQuery.Set("scope", "document")
	fillQuery.Set("id", "doc-1")
	fillQuery.Set("fill", "方法是什么？")
	filled := assistantReq(t, h, http.MethodGet, "/assistant?"+fillQuery.Encode(), "user-a", nil)
	body := filled.Body.String()
	if filled.Code != http.StatusOK || filled.Header().Get("Location") != "" {
		t.Fatalf("fill status %d location %q", filled.Code, filled.Header().Get("Location"))
	}
	if !strings.Contains(body, "<h2>快速开始</h2>") || strings.Contains(body, "接下来可以问") {
		t.Fatalf("fill should stay on quick start %s", body)
	}
	if !strings.Contains(body, "<textarea name=\"question\">方法是什么？</textarea>") {
		t.Fatalf("fill did not enter the box %s", body)
	}
	if !strings.Contains(body, ">方法是什么？</a>") || !strings.Contains(body, ">数据从哪来？</a>") {
		t.Fatalf("fill removed a suggestion %s", body)
	}

	form := url.Values{}
	form.Set("scope", "document")
	form.Set("id", "doc-1")
	form.Set("question", "  方法是什么？  ")
	posted := assistantReq(t, h, http.MethodPost, "/assistant", "user-a", form)
	if posted.Code != http.StatusSeeOther {
		t.Fatalf("submit %d %s", posted.Code, posted.Body.String())
	}
	loc, err := url.Parse(posted.Header().Get("Location"))
	if err != nil || loc.Query().Get("asked") != "方法是什么？" || loc.Query().Get("fill") != "" {
		t.Fatalf("redirect %q", posted.Header().Get("Location"))
	}

	after := assistantReq(t, h, http.MethodGet, posted.Header().Get("Location"), "user-a", nil)
	afterBody := after.Body.String()
	if after.Code != http.StatusOK || !strings.Contains(afterBody, "<h2>接下来可以问</h2>") {
		t.Fatalf("after answer %d %s", after.Code, afterBody)
	}
	if !strings.Contains(afterBody, "上一问：方法是什么？") || strings.Contains(afterBody, ">方法是什么？</a>") {
		t.Fatalf("asked question should leave the list %s", afterBody)
	}
	if !strings.Contains(afterBody, ">数据从哪来？</a>") || !strings.Contains(afterBody, `<textarea name="question"></textarea>`) {
		t.Fatalf("next suggestion %s", afterBody)
	}

	blank := url.Values{}
	blank.Set("scope", "document")
	blank.Set("id", "doc-1")
	blank.Set("question", "   ")
	emptyPost := assistantReq(t, h, http.MethodPost, "/assistant", "user-a", blank)
	if emptyPost.Code != http.StatusOK || !strings.Contains(emptyPost.Body.String(), "<h2>快速开始</h2>") {
		t.Fatalf("blank submit %d %s", emptyPost.Code, emptyPost.Body.String())
	}

	kb := assistantReq(t, h, http.MethodGet, "/assistant?scope=kb&id=kb-1", "user-a", nil)
	if kb.Code != http.StatusOK || !strings.Contains(kb.Body.String(), ">这个库在讲什么？</a>") {
		t.Fatalf("kb %d %s", kb.Code, kb.Body.String())
	}

	notReady := assistantReq(t, h, http.MethodGet, "/assistant?scope=document&id=doc-2", "user-a", nil)
	if notReady.Code != http.StatusOK || !strings.Contains(notReady.Body.String(), "暂无推荐问题") || strings.Contains(notReady.Body.String(), "还没完成的问题") {
		t.Fatalf("not ready %d %s", notReady.Code, notReady.Body.String())
	}

	if assistantReq(t, h, http.MethodGet, "/assistant?scope=document&id=doc-1", "user-b", nil).Code != http.StatusNotFound {
		t.Fatal("other user")
	}
	if assistantReq(t, h, http.MethodPost, "/assistant", "user-b", form).Code != http.StatusNotFound {
		t.Fatal("other user post")
	}
	if assistantReq(t, h, http.MethodGet, "/assistant?scope=document&id=doc-1", "", nil).Code != http.StatusUnauthorized {
		t.Fatal("anonymous")
	}
}

func TestAssistantFollowUpClickOnlyFills(t *testing.T) {
	m := suggest.NewMemory()
	m.PutDocument("doc-1", "user-a", true, []string{"方法是什么？", "数据从哪来？"})
	h := suggestAPI(m)
	query := url.Values{}
	query.Set("scope", "document")
	query.Set("id", "doc-1")
	query.Set("asked", "方法是什么？")
	query.Set("fill", "数据从哪来？")
	rec := assistantReq(t, h, http.MethodGet, "/assistant?"+query.Encode(), "user-a", nil)
	body := rec.Body.String()
	if rec.Code != http.StatusOK || rec.Header().Get("Location") != "" {
		t.Fatalf("status %d location %q", rec.Code, rec.Header().Get("Location"))
	}
	if !strings.Contains(body, "<h2>接下来可以问</h2>") || !strings.Contains(body, "上一问：方法是什么？") {
		t.Fatalf("follow-up heading %s", body)
	}
	if !strings.Contains(body, "<textarea name=\"question\">数据从哪来？</textarea>") || !strings.Contains(body, ">数据从哪来？</a>") {
		t.Fatalf("fill removed the next question %s", body)
	}
	if strings.Contains(body, ">方法是什么？</a>") || strings.Contains(body, "正在更新推荐问题") {
		t.Fatalf("asked question returned %s", body)
	}
}

func TestAssistantShowsKBRefreshNotice(t *testing.T) {
	m := suggest.NewMemory()
	m.PutKB("kb-1", "user-a", true, []string{"旧问题"})
	m.SetKBSource("kb-1", "user-a", "fp-2")
	h := suggestAPI(m)
	rec := assistantReq(t, h, http.MethodGet, "/assistant?scope=kb&id=kb-1", "user-a", nil)
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, "正在更新推荐问题") || !strings.Contains(body, ">旧问题</a>") {
		t.Fatalf("refresh notice %d %s", rec.Code, body)
	}
}
