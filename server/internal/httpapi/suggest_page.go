package httpapi

import (
	"html/template"
	"net/http"
	"net/url"
	"strings"

	"yanzhi/server/internal/suggest"
)

const (
	headingQuickStart = "快速开始"
	headingNext       = "接下来可以问"
)

type assistantLink struct {
	Text string
	Href string
}

type assistantView struct {
	Heading string
	Scope   string
	ID      string
	Draft   string
	Asked   string
	Status  string
	Links   []assistantLink
}

var assistantTmpl = template.Must(template.New("assistant").Parse(`<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<title>智能助手</title>
</head>
<body>
<h1>智能助手</h1>
{{if .Asked}}<p>上一问：{{.Asked}}</p>{{end}}
<h2>{{.Heading}}</h2>
{{if eq .Status "generating"}}<p>正在更新推荐问题</p>{{end}}
{{if .Links}}
<ul>
{{range .Links}}<li><a href="{{.Href}}">{{.Text}}</a></li>
{{end}}
</ul>
{{else}}
<p>暂无推荐问题</p>
{{end}}
<form method="post" action="/assistant">
<input type="hidden" name="scope" value="{{.Scope}}">
<input type="hidden" name="id" value="{{.ID}}">
<textarea name="question">{{.Draft}}</textarea>
<button type="submit">发送</button>
</form>
</body>
</html>
`))

func (h *suggestHandler) page(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	scope := query.Get("scope")
	id := query.Get("id")
	asked := strings.TrimSpace(query.Get("asked"))
	draft := strings.TrimSpace(query.Get("fill"))
	result, err := h.loadQuestions(r, scope, id, asked)
	if err != nil {
		writeSuggestErr(w, r, err)
		return
	}
	writeAssistantPage(w, assistantView{
		Heading: headingFor(asked),
		Scope:   scope,
		ID:      id,
		Draft:   draft,
		Asked:   asked,
		Status:  result.Status,
		Links:   assistantLinks(scope, id, asked, result.Questions),
	})
}

func (h *suggestHandler) submit(w http.ResponseWriter, r *http.Request) {
	scope := r.PostFormValue("scope")
	id := r.PostFormValue("id")
	question := strings.TrimSpace(r.PostFormValue("question"))
	result, err := h.loadQuestions(r, scope, id, "")
	if err != nil {
		writeSuggestErr(w, r, err)
		return
	}
	if question == "" {
		writeAssistantPage(w, assistantView{
			Heading: headingQuickStart,
			Scope:   scope,
			ID:      id,
			Status:  result.Status,
			Links:   assistantLinks(scope, id, "", result.Questions),
		})
		return
	}
	target := url.Values{}
	target.Set("scope", scope)
	target.Set("id", id)
	target.Set("asked", question)
	http.Redirect(w, r, "/assistant?"+target.Encode(), http.StatusSeeOther)
}

func (h *suggestHandler) loadQuestions(r *http.Request, scope, id, asked string) (suggest.Result, error) {
	caller, err := h.caller(r)
	if err != nil {
		return suggest.Result{}, err
	}
	return h.list.List(caller, scope, id, asked)
}

func headingFor(asked string) string {
	if asked == "" {
		return headingQuickStart
	}
	return headingNext
}

func assistantLinks(scope, id, asked string, questions []string) []assistantLink {
	links := make([]assistantLink, 0, len(questions))
	for _, question := range questions {
		query := url.Values{}
		query.Set("scope", scope)
		query.Set("id", id)
		query.Set("fill", question)
		if asked != "" {
			query.Set("asked", asked)
		}
		links = append(links, assistantLink{Text: question, Href: "/assistant?" + query.Encode()})
	}
	return links
}

func writeAssistantPage(w http.ResponseWriter, view assistantView) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = assistantTmpl.Execute(w, view)
}
