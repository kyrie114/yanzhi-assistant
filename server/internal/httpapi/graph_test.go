package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"yanzhi/server/internal/account"
	"yanzhi/server/internal/graph"
	"yanzhi/server/internal/platform/clock"
)

const graphSecret = "secret-graph-node"

type fakeBrowse struct {
	view graph.View
}

func (f fakeBrowse) Subgraph(context.Context, string, graph.LibraryFilter) (graph.View, error) {
	return f.view, nil
}

func (f fakeBrowse) Neighbors(context.Context, string, string) (graph.View, error) {
	return f.view, nil
}

func (f fakeBrowse) Node(context.Context, string, string) (graph.NodeDetail, error) {
	return graph.NodeDetail{Sources: []graph.SourcePage{}}, nil
}

func (f fakeBrowse) Edge(context.Context, string, string) (graph.EdgeDetail, error) {
	return graph.EdgeDetail{Evidence: []graph.Evidence{}}, nil
}

func (f fakeBrowse) Extraction(context.Context, string, string) (graph.Extraction, error) {
	return graph.Extraction{Stages: []graph.Stage{}}, nil
}

func (f fakeBrowse) Unresolved(context.Context, string, string) ([]string, error) {
	return []string{}, nil
}

type fakeRetry struct{}

func (fakeRetry) RetryGraph(context.Context, string, string, string) error { return nil }

func TestGraphRouteAdminIsNotFoundAndUserSeesView(t *testing.T) {
	svc := account.NewService(account.NewMemoryStore(), clock.System{}, []byte("test-secret"), bcrypt.MinCost)
	browse := fakeBrowse{view: graph.View{
		Nodes:     []graph.Node{{ID: "n1", Name: graphSecret, Type: "method"}},
		Edges:     []graph.Edge{},
		Truncated: false,
	}}
	h := NewHandler(svc, WithGraph(browse), WithGraphRetry(fakeRetry{}))
	userToken := registerAndLogin(t, h, "graph-user@example.edu.cn", "用户", "Abc12345")
	if _, err := svc.CreateAdmin("graph-admin@example.edu.cn", "管理员", "Abc12345"); err != nil {
		t.Fatal(err)
	}
	status, _, body := do(t, h, http.MethodPost, "/api/v1/auth/login", "", map[string]string{
		"email": "graph-admin@example.edu.cn", "password": "Abc12345",
	})
	if status != http.StatusOK {
		t.Fatalf("admin login %d %s", status, body)
	}
	var login map[string]any
	decode(t, body, &login)
	adminToken, _ := login["access_token"].(string)
	if adminToken == "" {
		t.Fatalf("missing admin token %s", body)
	}

	status, _, body = do(t, h, http.MethodGet, "/api/v1/graph", adminToken, nil)
	mustError(t, status, body, http.StatusNotFound, "NOT_FOUND", "资源不存在")
	if strings.Contains(string(body), graphSecret) {
		t.Fatalf("admin body contains the node name: %s", body)
	}

	status, _, body = do(t, h, http.MethodGet, "/api/v1/graph", userToken, nil)
	if status != http.StatusOK {
		t.Fatalf("user graph %d %s", status, body)
	}
	var view graph.View
	decode(t, body, &view)
	if len(view.Nodes) != 1 || view.Nodes[0].Name != graphSecret || view.Edges == nil {
		t.Fatalf("view %+v", view)
	}

	plain := NewHandler(svc)
	status, _, body = do(t, plain, http.MethodGet, "/api/v1/graph", userToken, nil)
	if status != http.StatusNotFound {
		t.Fatalf("unmounted status %d %s", status, body)
	}
	var apiErr apiError
	if json.Unmarshal(body, &apiErr) == nil && apiErr.Code == "NOT_FOUND" {
		t.Fatalf("unmounted graph route returned JSON not found: %s", body)
	}
}
