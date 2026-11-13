package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClientRerankSortsAndDeduplicates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rerank" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"results": []map[string]any{
				{"index": 0, "relevance_score": 0.2},
				{"index": 2, "relevance_score": 0.9},
				{"index": 2, "relevance_score": 0.8},
				{"index": 99, "relevance_score": 1.0},
				{"index": 1, "relevance_score": 0.5},
			},
		})
	}))
	defer srv.Close()

	res, _, err := (Client{}).Rerank(context.Background(), Remote{
		Kind: "rerank", BaseURL: srv.URL, Model: "bge-rerank", APIKey: "sk-test",
	}, "query", []string{"d0", "d1", "d2"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 3 || res[0].Index != 2 || res[1].Index != 1 || res[2].Index != 0 {
		t.Fatalf("unexpected rerank order: %+v", res)
	}
}

func TestMemoryGatewayRerankFallbackAndLogging(t *testing.T) {
	gw := NewMemoryGateway()
	indices, status := gw.Rerank(context.Background(), CallMeta{RequestID: "req-1"}, "q", []string{"a", "b"})
	if status != "skipped" || indices != nil {
		t.Fatalf("expected skipped when rerank unconfigured, got %s %v", status, indices)
	}

	gw.SetModel("rerank", Remote{Model: "rerank-v1"})
	gw.RerankFn = func(ctx context.Context, query string, docs []string) ([]int, error) {
		return nil, context.DeadlineExceeded
	}
	indices, status = gw.Rerank(context.Background(), CallMeta{RequestID: "req-2"}, "q", []string{"a", "b"})
	if status != "error" || indices != nil {
		t.Fatalf("expected error status on failure, got %s %v", status, indices)
	}
	calls := gw.Calls()
	if len(calls) != 2 || calls[0].Status != "skipped" || calls[1].Status != "error" {
		t.Fatalf("unexpected call records: %+v", calls)
	}
}
