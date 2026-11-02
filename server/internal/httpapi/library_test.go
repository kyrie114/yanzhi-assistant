package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"yanzhi/server/internal/identity"
	"yanzhi/server/internal/ingest"
	"yanzhi/server/internal/jobs"
	"yanzhi/server/internal/knowledge"
	"yanzhi/server/internal/platform/clock"
	"yanzhi/server/internal/platform/db"
	"yanzhi/server/internal/platform/docreader"
	"yanzhi/server/internal/platform/files"
	"yanzhi/server/internal/provider"
)

func TestUploadUsesDocreaderWhenDatabaseIsConfigured(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	py := os.Getenv("DOCREADER_PY")
	root := os.Getenv("DOCREADER_ROOT")
	if url == "" || py == "" || root == "" {
		t.Skip("TEST_DATABASE_URL, DOCREADER_PY, and DOCREADER_ROOT are required")
	}
	ctx := context.Background()
	migrations := os.Getenv("MIGRATIONS_DIR")
	if migrations == "" {
		migrations = filepath.Join("..", "..", "migrations")
	}
	if err := db.Migrate(ctx, url, migrations); err != nil {
		t.Fatal(err)
	}
	pool, err := db.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	secret := []byte("test-secret-with-at-least-32-bytes")
	accounts, err := identity.NewService(pool, secret, 10, clock.System{})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	library := knowledge.Service{Pool: pool, Files: files.Store{Root: dir}}
	key := bytes.Repeat([]byte{7}, 32)
	models := provider.Store{Pool: pool, Key: key}
	handler := NewLibraryHandler(accounts, library, models)
	token := registerLibraryUser(t, handler)
	promoteAdmin(t, handler, pool, token)
	embed := fakeEmbedder(t)
	defer embed.Close()
	saveModel(t, handler, token, embed.URL)

	rec := jsonRequest(t, handler, http.MethodPost, "/api/v1/kbs", token, map[string]string{"name": "论文"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create kb: %d %s", rec.Code, rec.Body.Bytes())
	}
	var kb struct {
		ID string `json:"id"`
	}
	decode(t, rec.Body.Bytes(), &kb)

	pdf := textPDF(t, py, root)
	rec = uploadPDF(t, handler, "/api/v1/kbs/"+kb.ID+"/documents", token, pdf)
	if rec.Code != http.StatusCreated {
		t.Fatalf("upload: %d %s", rec.Code, rec.Body.Bytes())
	}
	var doc struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	decode(t, rec.Body.Bytes(), &doc)
	if doc.Status != "待处理" {
		t.Fatalf("status %s", doc.Status)
	}

	addr, cmd := startParser(t, py, root)
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()
	var job jobs.Job
	if err := pool.QueryRow(ctx, `
		SELECT id::text, kind, owner_id::text, target_id::text, request_id::text, attempt
		FROM jobs WHERE target_id = $1::uuid AND kind = 'ingest'`, db.MustUUID(doc.ID)).Scan(
		&job.ID, &job.Kind, &job.OwnerID, &job.TargetID, &job.RequestID, &job.Attempt); err != nil {
		t.Fatal(err)
	}
	runner := ingest.Runner{
		Pool: pool, Files: library.Files,
		Reader: docreader.New(addr, "live-token", 10*time.Second),
		Models: models, AI: provider.Client{},
		Chunk:  ingest.DefaultConfig(), EmbedBatch: 16, Timeout: 10 * time.Second,
	}
	if err := runner.Run(ctx, job); err != nil {
		t.Fatal(err)
	}
	rec = jsonRequest(t, handler, http.MethodGet, "/api/v1/documents/"+doc.ID, token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("document: %d %s", rec.Code, rec.Body.Bytes())
	}
	var got struct {
		Status        string  `json:"status"`
		FailureReason *string `json:"failure_reason"`
	}
	decode(t, rec.Body.Bytes(), &got)
	if got.Status != "已完成" || got.FailureReason != nil {
		t.Fatalf("after parse and embed: %+v", got)
	}
}

func promoteAdmin(t *testing.T, handler http.Handler, pool *pgxpool.Pool, token string) {
	t.Helper()
	rec := jsonRequest(t, handler, http.MethodGet, "/api/v1/me", token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("me: %d %s", rec.Code, rec.Body.Bytes())
	}
	var me struct {
		ID string `json:"id"`
	}
	decode(t, rec.Body.Bytes(), &me)
	if _, err := pool.Exec(context.Background(), `UPDATE users SET role = 'admin' WHERE id = $1::uuid`, db.MustUUID(me.ID)); err != nil {
		t.Fatal(err)
	}
}

func saveModel(t *testing.T, handler http.Handler, token, base string) {
	t.Helper()
	dim := 4
	rec := jsonRequest(t, handler, http.MethodPut, "/api/v1/admin/model-configs/embedding", token, map[string]any{
		"base_url": base + "/v1", "model_name": "embed-test", "api_key": "sk-test", "dimension": dim,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("model: %d %s", rec.Code, rec.Body.Bytes())
	}
}

func fakeEmbedder(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Input json.RawMessage `json:"input"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		n := 1
		if len(req.Input) > 0 && req.Input[0] == '[' {
			var texts []string
			if json.Unmarshal(req.Input, &texts) == nil && len(texts) > 0 {
				n = len(texts)
			}
		}
		data := make([]map[string]any, n)
		for i := range data {
			data[i] = map[string]any{"index": i, "embedding": []float32{0.1, 0.2, 0.3, 0.4}}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": data, "usage": map[string]int{"prompt_tokens": n},
		})
	}))
}

func registerLibraryUser(t *testing.T, handler http.Handler) string {
	t.Helper()
	email := "lib-" + newRequestUUID()[:8] + "@example.com"
	rec := jsonRequest(t, handler, http.MethodPost, "/api/v1/auth/register", "", map[string]string{
		"email": email, "name": "读者", "password": "password1",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("register: %d %s", rec.Code, rec.Body.Bytes())
	}
	rec = jsonRequest(t, handler, http.MethodPost, "/api/v1/auth/login", "", map[string]string{
		"email": email, "password": "password1",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("login: %d %s", rec.Code, rec.Body.Bytes())
	}
	var body struct {
		AccessToken string `json:"access_token"`
	}
	decode(t, rec.Body.Bytes(), &body)
	return body.AccessToken
}

func jsonRequest(t *testing.T, handler http.Handler, method, path, token string, payload any) *httptest.ResponseRecorder {
	t.Helper()
	var body io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		body = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, body)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func uploadPDF(t *testing.T, handler http.Handler, path, token string, pdf []byte) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	form := multipart.NewWriter(&buf)
	part, err := form.CreateFormFile("file", "paper.pdf")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(pdf); err != nil {
		t.Fatal(err)
	}
	if err := form.WriteField("title", "论文摘录"); err != nil {
		t.Fatal(err)
	}
	if err := form.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, path, &buf)
	req.Header.Set("Content-Type", form.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func textPDF(t *testing.T, py, root string) []byte {
	t.Helper()
	cmd := exec.Command(py, "-c", "from test_pdf import pdf_bytes; import os; from pathlib import Path; Path(os.environ['PDF_OUT']).write_bytes(pdf_bytes(['Alpha knowledge base']))")
	out := filepath.Join(t.TempDir(), "paper.pdf")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "PDF_OUT="+out, "PYTHONPATH="+root+string(os.PathListSeparator)+filepath.Join(root, "tests"))
	if msg, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("pdf: %v %s", err, msg)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func startParser(t *testing.T, py, root string) (string, *exec.Cmd) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(py, "-m", "docreader")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "DOCREADER_LISTEN="+addr, "DOCREADER_TOKEN=live-token")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	client := docreader.New(addr, "live-token", time.Second)
	for time.Now().Before(deadline) {
		if client.Healthy(context.Background()) {
			return addr, cmd
		}
		time.Sleep(100 * time.Millisecond)
	}
	_ = cmd.Process.Kill()
	t.Fatal("parser did not start")
	return "", nil
}
