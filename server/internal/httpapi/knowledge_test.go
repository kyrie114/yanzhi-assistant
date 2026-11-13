package httpapi

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"yanzhi/server/internal/account"
	"yanzhi/server/internal/apperr"
	"yanzhi/server/internal/knowledge"
)

type testRig struct {
	accStore *account.MemoryStore
	accSvc   *account.Service
	kbStore  *knowledge.MemoryStore
	handler  http.Handler
}

func newTestRig(restrictEmail bool) *testRig {
	accStore := account.NewMemoryStore()
	accSvc := account.NewService(accStore, nil, []byte("test-secret-with-at-least-32-bytes-long"), 4)
	kbStore := knowledge.NewMemoryStore(accStore, nil, nil)
	h := NewHandler(accSvc,
		WithKnowledge(kbStore),
		WithEmailRestriction(restrictEmail),
	)
	return &testRig{
		accStore: accStore,
		accSvc:   accSvc,
		kbStore:  kbStore,
		handler:  h,
	}
}

func (r *testRig) registerUser(t *testing.T, email, name, password string, verify bool) (account.User, string) {
	t.Helper()
	u, err := r.accSvc.Register(email, name, password)
	if err != nil {
		t.Fatalf("register failed: %v", err)
	}
	if verify {
		now := time.Now().UTC()
		if err := r.accStore.SetEmailVerified(u.ID, &now); err != nil {
			t.Fatalf("verify failed: %v", err)
		}
	}
	res, err := r.accSvc.Login(email, password)
	if err != nil {
		t.Fatalf("login failed: %v", err)
	}
	return u, res.AccessToken
}

func (r *testRig) registerAdmin(t *testing.T, email, name, password string) (account.User, string) {
	t.Helper()
	u, err := r.accSvc.CreateAdmin(email, name, password)
	if err != nil {
		t.Fatalf("create admin failed: %v", err)
	}
	res, err := r.accSvc.Login(email, password)
	if err != nil {
		t.Fatalf("admin login failed: %v", err)
	}
	return u, res.AccessToken
}

func (r *testRig) doReq(t *testing.T, method, path, token string, body []byte, contentType string) (*http.Response, []byte) {
	t.Helper()
	var bodyReader io.Reader
	if body != nil {
		bodyReader = bytes.NewReader(body)
	}
	req := httptest.NewRequest(method, path, bodyReader)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	rec := httptest.NewRecorder()
	r.handler.ServeHTTP(rec, req)
	resp := rec.Result()
	out, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return resp, out
}

func (r *testRig) doJSON(t *testing.T, method, path, token string, body any) (*http.Response, []byte) {
	t.Helper()
	var b []byte
	if body != nil {
		var err error
		b, err = json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
	}
	return r.doReq(t, method, path, token, b, "application/json")
}

func makeSamplePDF(pages int) []byte {
	var b strings.Builder
	b.WriteString("%PDF-1.4\n1 0 obj\n<< /Type /Catalog >>\nendobj\n")
	for i := 1; i <= pages; i++ {
		b.WriteString(fmt.Sprintf("%d 0 obj\n<< /Type /Page >>\nendobj\n", i+1))
	}
	b.WriteString("trailer\n<< /Root 1 0 R >>\n%%EOF")
	return []byte(b.String())
}

func buildUploadMultipart(fileName string, fileBytes []byte, fields map[string]string) (body []byte, contentType string) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for k, v := range fields {
		_ = w.WriteField(k, v)
	}
	if fileBytes != nil || fileName != "" {
		part, _ := w.CreateFormFile("file", fileName)
		if fileBytes != nil {
			_, _ = part.Write(fileBytes)
		}
	}
	_ = w.Close()
	return buf.Bytes(), w.FormDataContentType()
}

func TestKBCreationAndValidation(t *testing.T) {
	rig := newTestRig(false)
	_, token := rig.registerUser(t, "user@example.com", "User", "Pass123456", true)

	resp, body := rig.doJSON(t, "POST", "/api/v1/kbs", token, map[string]string{"name": ""})
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 for empty name, got %d: %s", resp.StatusCode, body)
	}
	var errResp errorBody
	_ = json.Unmarshal(body, &errResp)
	if errResp.Code != "VALIDATION_ERROR" || errResp.Details["field"] != "name" {
		t.Fatalf("unexpected error response: %+v", errResp)
	}

	resp, _ = rig.doJSON(t, "POST", "/api/v1/kbs", token, map[string]string{"name": strings.Repeat("a", 51)})
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 for name > 50 characters, got %d", resp.StatusCode)
	}

	resp, body = rig.doJSON(t, "POST", "/api/v1/kbs", token, map[string]string{"name": "论文阅读"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201 for valid kb, got %d: %s", resp.StatusCode, body)
	}
	var created knowledge.KB
	_ = json.Unmarshal(body, &created)
	if created.Name != "论文阅读" || !created.GraphEnabled || created.ID == "" {
		t.Fatalf("unexpected created KB: %+v", created)
	}

	resp, body = rig.doJSON(t, "GET", "/api/v1/kbs", token, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var kbs []knowledge.KB
	_ = json.Unmarshal(body, &kbs)
	if len(kbs) != 1 || kbs[0].ID != created.ID {
		t.Fatalf("expected 1 kb in list, got %+v", kbs)
	}

	resp, body = rig.doJSON(t, "GET", "/api/v1/kbs/"+created.ID, token, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var gotKB knowledge.KB
	_ = json.Unmarshal(body, &gotKB)
	if gotKB.ID != created.ID || gotKB.Name != "论文阅读" {
		t.Fatalf("unexpected get KB: %+v", gotKB)
	}

	resp, _ = rig.doJSON(t, "GET", "/api/v1/kbs/non-existent-id", token, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 for invalid kb id, got %d", resp.StatusCode)
	}
}

func TestUploadAcceptanceCriteria(t *testing.T) {
	rig := newTestRig(false)
	userA, tokenA := rig.registerUser(t, "usera@example.com", "User A", "Pass123456", true)
	_, tokenB := rig.registerUser(t, "userb@example.com", "User B", "Pass123456", true)

	resp, body := rig.doJSON(t, "POST", "/api/v1/kbs", tokenA, map[string]string{"name": "论文阅读"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create kb: %d %s", resp.StatusCode, body)
	}
	var kb knowledge.KB
	_ = json.Unmarshal(body, &kb)

	twentyPagePDF := makeSamplePDF(20)
	twelveMBPDF := make([]byte, 12*1024*1024)
	copy(twelveMBPDF, twentyPagePDF)

	fields := map[string]string{
		"title":   "注意力机制综述",
		"authors": `["Vaswani", "Jones"]`,
		"year":    "2023",
		"venue":   "NeurIPS",
	}
	uploadBody, ct := buildUploadMultipart("paper.pdf", twelveMBPDF, fields)
	resp, body = rig.doReq(t, "POST", "/api/v1/kbs/"+kb.ID+"/documents", tokenA, uploadBody, ct)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("AC-02-01 failed: expected 201, got %d: %s", resp.StatusCode, body)
	}
	var doc1 knowledge.Doc
	_ = json.Unmarshal(body, &doc1)
	if doc1.Title != "注意力机制综述" || doc1.Status != "待处理" || len(doc1.Authors) != 2 || *doc1.Year != 2023 {
		t.Fatalf("unexpected doc metadata: %+v", doc1)
	}

	docxBody, ctDocx := buildUploadMultipart("paper.docx", twelveMBPDF, fields)
	resp, body = rig.doReq(t, "POST", "/api/v1/kbs/"+kb.ID+"/documents", tokenA, docxBody, ctDocx)
	if resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("AC-02-03 failed: expected 415 for docx, got %d: %s", resp.StatusCode, body)
	}

	fakePDFBody, ctFake := buildUploadMultipart("fake.pdf", []byte("NOT-PDF-HEADER-CONTENT"), fields)
	resp, body = rig.doReq(t, "POST", "/api/v1/kbs/"+kb.ID+"/documents", tokenA, fakePDFBody, ctFake)
	if resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("AC-02-03 failed: expected 415 for fake pdf, got %d: %s", resp.StatusCode, body)
	}

	fiftyOneMB := make([]byte, 51*1024*1024)
	copy(fiftyOneMB, twentyPagePDF)
	largeBody, ctLarge := buildUploadMultipart("large.pdf", fiftyOneMB, fields)
	resp, body = rig.doReq(t, "POST", "/api/v1/kbs/"+kb.ID+"/documents", tokenA, largeBody, ctLarge)
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("AC-02-04 failed: expected 413 for 51MB, got %d: %s", resp.StatusCode, body)
	}

	exactFiftyMB := make([]byte, apperr.MaxUploadBytes)
	copy(exactFiftyMB, twentyPagePDF)
	fieldsBoundary := map[string]string{
		"title": "边界大文件",
	}
	boundaryBody, ctBoundary := buildUploadMultipart("boundary.pdf", exactFiftyMB, fieldsBoundary)
	resp, body = rig.doReq(t, "POST", "/api/v1/kbs/"+kb.ID+"/documents", tokenA, boundaryBody, ctBoundary)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("AC-02-05 failed: expected 201 for exactly 50MB, got %d: %s", resp.StatusCode, body)
	}

	threeFiftyPages := makeSamplePDF(350)
	tooManyBody, ctTooMany := buildUploadMultipart("pages.pdf", threeFiftyPages, fields)
	resp, body = rig.doReq(t, "POST", "/api/v1/kbs/"+kb.ID+"/documents", tokenA, tooManyBody, ctTooMany)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("AC-02-06 failed: expected 422 for 350 pages, got %d: %s", resp.StatusCode, body)
	}
	var pagesErr errorBody
	_ = json.Unmarshal(body, &pagesErr)
	if pagesErr.Code != "TOO_MANY_PAGES" {
		t.Fatalf("expected TOO_MANY_PAGES, got %s", pagesErr.Code)
	}

	encryptedPDF := []byte("%PDF-1.4\n/Encrypt 2 0 R\n%%EOF")
	encBody, ctEnc := buildUploadMultipart("enc.pdf", encryptedPDF, fields)
	resp, body = rig.doReq(t, "POST", "/api/v1/kbs/"+kb.ID+"/documents", tokenA, encBody, ctEnc)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("AC-02-09 failed: expected 422 for encrypted, got %d: %s", resp.StatusCode, body)
	}
	var encErr errorBody
	_ = json.Unmarshal(body, &encErr)
	if encErr.Code != "FILE_ENCRYPTED" {
		t.Fatalf("expected FILE_ENCRYPTED, got %s", encErr.Code)
	}

	_ = rig.accStore.SetStorageBytes(userA.ID, 2137000000)
	twentyMB := make([]byte, 20971520)
	copy(twentyMB, twentyPagePDF)
	quotaBody, ctQuota := buildUploadMultipart("quota.pdf", twentyMB, fields)
	resp, body = rig.doReq(t, "POST", "/api/v1/kbs/"+kb.ID+"/documents", tokenA, quotaBody, ctQuota)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("AC-02-08 failed: expected 422 for quota, got %d: %s", resp.StatusCode, body)
	}
	var quotaErr errorBody
	_ = json.Unmarshal(body, &quotaErr)
	if quotaErr.Code != "STORAGE_QUOTA_EXCEEDED" {
		t.Fatalf("expected STORAGE_QUOTA_EXCEEDED, got %s", quotaErr.Code)
	}
	_ = rig.accStore.SetStorageBytes(userA.ID, 0)

	resp, body = rig.doReq(t, "POST", "/api/v1/kbs/"+kb.ID+"/documents", tokenA, uploadBody, ct)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("AC-02-07 failed: expected 409 for duplicate upload, got %d: %s", resp.StatusCode, body)
	}
	var dupErr errorBody
	_ = json.Unmarshal(body, &dupErr)
	if dupErr.Code != "DUPLICATE_DOCUMENT" || dupErr.Details["document_id"] != doc1.ID || dupErr.Details["kb_id"] != kb.ID {
		t.Fatalf("expected DUPLICATE_DOCUMENT with doc1 details, got %+v", dupErr)
	}

	failMsg := "simulated failure"
	if err := rig.kbStore.SetDocStatusForTest(userA.ID, doc1.ID, "failed", &failMsg); err != nil {
		t.Fatal(err)
	}

	fieldsReused := map[string]string{
		"title":   "注意力机制综述重试",
		"authors": `["Vaswani"]`,
		"year":    "2024",
	}
	reusedBody, ctReused := buildUploadMultipart("paper.pdf", twelveMBPDF, fieldsReused)
	resp, body = rig.doReq(t, "POST", "/api/v1/kbs/"+kb.ID+"/documents", tokenA, reusedBody, ctReused)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("AC-02-12 failed: expected 200 for failed reuse, got %d: %s", resp.StatusCode, body)
	}
	var reusedDoc knowledge.Doc
	_ = json.Unmarshal(body, &reusedDoc)
	if reusedDoc.ID != doc1.ID || reusedDoc.Status != "待处理" || reusedDoc.Title != "注意力机制综述重试" {
		t.Fatalf("failed doc was not properly reused: %+v", reusedDoc)
	}

	resp, _ = rig.doReq(t, "POST", "/api/v1/kbs/"+kb.ID+"/documents", tokenB, uploadBody, ct)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 uploading to foreign KB, got %d", resp.StatusCode)
	}
}

func TestIdempotencyReplayAndConflict(t *testing.T) {
	rig := newTestRig(false)
	_, token := rig.registerUser(t, "idemp@example.com", "Idemp User", "Pass123456", true)

	resp, body := rig.doJSON(t, "POST", "/api/v1/kbs", token, map[string]string{"name": "Idemp KB"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatal(resp.StatusCode)
	}
	var kb knowledge.KB
	_ = json.Unmarshal(body, &kb)

	pdf := makeSamplePDF(5)
	fields := map[string]string{
		"title": "Idemp Paper",
	}
	uploadBody, ct := buildUploadMultipart("idemp.pdf", pdf, fields)

	req := httptest.NewRequest("POST", "/api/v1/kbs/"+kb.ID+"/documents", bytes.NewReader(uploadBody))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", ct)
	req.Header.Set("Idempotency-Key", "idemp-key-123")
	rec := httptest.NewRecorder()
	rig.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201 on first upload, got %d", rec.Code)
	}
	var doc1 knowledge.Doc
	_ = json.Unmarshal(rec.Body.Bytes(), &doc1)

	req2 := httptest.NewRequest("POST", "/api/v1/kbs/"+kb.ID+"/documents", bytes.NewReader(uploadBody))
	req2.Header.Set("Authorization", "Bearer "+token)
	req2.Header.Set("Content-Type", ct)
	req2.Header.Set("Idempotency-Key", "idemp-key-123")
	rec2 := httptest.NewRecorder()
	rig.handler.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusCreated {
		t.Fatalf("expected 201 replay on identical upload, got %d", rec2.Code)
	}
	var docReplay knowledge.Doc
	_ = json.Unmarshal(rec2.Body.Bytes(), &docReplay)
	if docReplay.ID != doc1.ID {
		t.Fatalf("replay doc ID mismatch: %s vs %s", docReplay.ID, doc1.ID)
	}

	conflictFields := map[string]string{
		"title": "Conflicting Title",
	}
	conflictBody, ctConflict := buildUploadMultipart("idemp.pdf", pdf, conflictFields)
	req3 := httptest.NewRequest("POST", "/api/v1/kbs/"+kb.ID+"/documents", bytes.NewReader(conflictBody))
	req3.Header.Set("Authorization", "Bearer "+token)
	req3.Header.Set("Content-Type", ctConflict)
	req3.Header.Set("Idempotency-Key", "idemp-key-123")
	rec3 := httptest.NewRecorder()
	rig.handler.ServeHTTP(rec3, req3)
	if rec3.Code != http.StatusConflict {
		t.Fatalf("expected 409 conflict on different body hash, got %d", rec3.Code)
	}
	var errResp errorBody
	_ = json.Unmarshal(rec3.Body.Bytes(), &errResp)
	if errResp.Code != "IDEMPOTENCY_CONFLICT" {
		t.Fatalf("expected IDEMPOTENCY_CONFLICT, got %s", errResp.Code)
	}
	_ = sha256.Sum256([]byte("dummy"))
	_ = hex.EncodeToString([]byte("dummy"))
}

func TestDocListSearchPatchDeleteDownload(t *testing.T) {
	rig := newTestRig(false)
	userA, tokenA := rig.registerUser(t, "alice@example.com", "Alice", "Pass123456", true)
	_, tokenB := rig.registerUser(t, "bob@example.com", "Bob", "Pass123456", true)

	resp, body := rig.doJSON(t, "POST", "/api/v1/kbs", tokenA, map[string]string{"name": "Papers"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatal(resp.StatusCode)
	}
	var kb knowledge.KB
	_ = json.Unmarshal(body, &kb)

	pdfBase := makeSamplePDF(10)
	var docIDs []string
	var targetDocID string

	for i := 1; i <= 25; i++ {
		pdf := append(pdfBase, []byte(fmt.Sprintf("\n%% doc %d", i))...)
		title := fmt.Sprintf("Doc %d", i)
		if i == 5 {
			title = "知识图谱导论"
		} else if i == 12 {
			title = "旧标题"
			targetDocID = ""
		}
		uploadBody, ct := buildUploadMultipart(fmt.Sprintf("doc_%d.pdf", i), pdf, map[string]string{"title": title})
		resp, b := rig.doReq(t, "POST", "/api/v1/kbs/"+kb.ID+"/documents", tokenA, uploadBody, ct)
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("upload %d failed: %d %s", i, resp.StatusCode, b)
		}
		var d knowledge.Doc
		_ = json.Unmarshal(b, &d)
		docIDs = append(docIDs, d.ID)
		if i == 12 {
			targetDocID = d.ID
		}
	}

	resp, body = rig.doJSON(t, "PATCH", "/api/v1/documents/"+targetDocID, tokenA, map[string]string{"title": "图谱推理新进展"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("patch failed: %d %s", resp.StatusCode, body)
	}
	var patchedDoc knowledge.Doc
	_ = json.Unmarshal(body, &patchedDoc)
	if patchedDoc.Title != "图谱推理新进展" {
		t.Fatalf("expected new title, got %s", patchedDoc.Title)
	}

	resp, body = rig.doJSON(t, "GET", "/api/v1/documents/"+targetDocID, tokenA, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get doc: %d", resp.StatusCode)
	}
	var detail knowledge.Doc
	_ = json.Unmarshal(body, &detail)
	if detail.Title != "图谱推理新进展" {
		t.Fatalf("detail title mismatch: %s", detail.Title)
	}

	searchURL := "/api/v1/documents?q=" + url.QueryEscape("图谱") + "&page=1&page_size=10"
	resp, body = rig.doJSON(t, "GET", searchURL, tokenA, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("search failed: %d", resp.StatusCode)
	}
	var listResp struct {
		Items    []knowledge.Doc `json:"items"`
		Page     int             `json:"page"`
		PageSize int             `json:"page_size"`
		Total    int             `json:"total"`
	}
	_ = json.Unmarshal(body, &listResp)
	if listResp.Total != 2 || len(listResp.Items) != 2 {
		t.Fatalf("AC-02-10 failed: expected 2 docs matching '图谱', got total %d, items %d", listResp.Total, len(listResp.Items))
	}

	resp, _ = rig.doJSON(t, "GET", "/api/v1/documents/"+targetDocID, tokenB, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("AC-08-01 failed: expected 404 for foreign doc access, got %d", resp.StatusCode)
	}

	resp, _ = rig.doJSON(t, "PATCH", "/api/v1/documents/"+targetDocID, tokenB, map[string]string{"title": "Hacked"})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 for foreign doc patch, got %d", resp.StatusCode)
	}

	resp, fileBytes := rig.doReq(t, "GET", "/api/v1/documents/"+targetDocID+"/file", tokenA, nil, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("file download: %d", resp.StatusCode)
	}
	if resp.Header.Get("Content-Type") != "application/pdf" {
		t.Fatalf("expected application/pdf, got %s", resp.Header.Get("Content-Type"))
	}
	cd := resp.Header.Get("Content-Disposition")
	if !strings.Contains(cd, "图谱推理新进展") {
		t.Fatalf("content disposition missing title: %s", cd)
	}
	if len(fileBytes) == 0 {
		t.Fatal("empty file downloaded")
	}

	resp, _ = rig.doReq(t, "GET", "/api/v1/documents/"+targetDocID+"/file", tokenB, nil, "")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("AC-08-01 failed: expected 404 for foreign file download, got %d", resp.StatusCode)
	}

	resp, _ = rig.doJSON(t, "DELETE", "/api/v1/documents/"+targetDocID, tokenB, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("AC-04-03 failed: expected 404 for foreign doc delete, got %d", resp.StatusCode)
	}

	resp, _ = rig.doJSON(t, "DELETE", "/api/v1/documents/"+targetDocID, tokenA, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("AC-04-01 failed: expected 200 for doc delete, got %d", resp.StatusCode)
	}

	resp, _ = rig.doJSON(t, "GET", "/api/v1/documents/"+targetDocID, tokenA, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 after delete, got %d", resp.StatusCode)
	}

	resp, _ = rig.doReq(t, "GET", "/api/v1/documents/"+targetDocID+"/file", tokenA, nil, "")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("file download should 404 after delete, got %d", resp.StatusCode)
	}

	embeddingDocID := docIDs[0]
	if err := rig.kbStore.SetDocStatusForTest(userA.ID, embeddingDocID, "embedding", nil); err != nil {
		t.Fatal(err)
	}
	resp, _ = rig.doJSON(t, "DELETE", "/api/v1/documents/"+embeddingDocID, tokenA, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("AC-04-04 failed: expected 200 deleting processing doc, got %d", resp.StatusCode)
	}
	resp, _ = rig.doJSON(t, "GET", "/api/v1/documents/"+embeddingDocID, tokenA, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("processing doc should be gone after delete, got %d", resp.StatusCode)
	}
}

func TestTagsAcceptanceCriteria(t *testing.T) {
	rig := newTestRig(false)
	_, tokenA := rig.registerUser(t, "tag_a@example.com", "Tag A", "Pass123456", true)
	_, tokenB := rig.registerUser(t, "tag_b@example.com", "Tag B", "Pass123456", true)

	resp, _ := rig.doJSON(t, "POST", "/api/v1/tags", tokenA, map[string]string{"name": ""})
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("AC-05-01: empty tag name expected 422, got %d", resp.StatusCode)
	}

	resp, body := rig.doJSON(t, "POST", "/api/v1/tags", tokenA, map[string]string{"name": "检索"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("AC-05-01: create tag expected 201, got %d: %s", resp.StatusCode, body)
	}
	var tag1 knowledge.Tag
	_ = json.Unmarshal(body, &tag1)
	if tag1.Name != "检索" || tag1.ID == "" {
		t.Fatalf("unexpected tag1: %+v", tag1)
	}

	resp, body = rig.doJSON(t, "POST", "/api/v1/tags", tokenA, map[string]string{"name": "检索"})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("duplicate tag expected 409, got %d: %s", resp.StatusCode, body)
	}
	var conflictErr errorBody
	_ = json.Unmarshal(body, &conflictErr)
	if conflictErr.Code != "TAG_EXISTS" {
		t.Fatalf("expected TAG_EXISTS, got %s", conflictErr.Code)
	}

	resp, body = rig.doJSON(t, "POST", "/api/v1/tags", tokenA, map[string]string{"name": "图神经网络"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create tag2: %d", resp.StatusCode)
	}
	var tag2 knowledge.Tag
	_ = json.Unmarshal(body, &tag2)

	resp, body = rig.doJSON(t, "POST", "/api/v1/kbs", tokenA, map[string]string{"name": "论文阅读"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatal(resp.StatusCode)
	}
	var kb knowledge.KB
	_ = json.Unmarshal(body, &kb)

	pdf := makeSamplePDF(5)
	uploadBodyX, ctX := buildUploadMultipart("x.pdf", pdf, map[string]string{"title": "Doc X"})
	resp, body = rig.doReq(t, "POST", "/api/v1/kbs/"+kb.ID+"/documents", tokenA, uploadBodyX, ctX)
	if resp.StatusCode != http.StatusCreated {
		t.Fatal(resp.StatusCode)
	}
	var docX knowledge.Doc
	_ = json.Unmarshal(body, &docX)

	pdfY := append(pdf, []byte("\n% y")...)
	uploadBodyY, ctY := buildUploadMultipart("y.pdf", pdfY, map[string]string{"title": "Doc Y"})
	resp, body = rig.doReq(t, "POST", "/api/v1/kbs/"+kb.ID+"/documents", tokenA, uploadBodyY, ctY)
	if resp.StatusCode != http.StatusCreated {
		t.Fatal(resp.StatusCode)
	}
	var docY knowledge.Doc
	_ = json.Unmarshal(body, &docY)

	pdfZ := append(pdf, []byte("\n% z")...)
	uploadBodyZ, ctZ := buildUploadMultipart("z.pdf", pdfZ, map[string]string{"title": "Doc Z"})
	resp, body = rig.doReq(t, "POST", "/api/v1/kbs/"+kb.ID+"/documents", tokenA, uploadBodyZ, ctZ)
	if resp.StatusCode != http.StatusCreated {
		t.Fatal(resp.StatusCode)
	}
	var docZ knowledge.Doc
	_ = json.Unmarshal(body, &docZ)

	resp, body = rig.doJSON(t, "PUT", "/api/v1/documents/"+docX.ID+"/tags", tokenA, map[string][]string{
		"tag_ids": {tag1.ID, tag2.ID},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("AC-05-02 failed: expected 200, got %d: %s", resp.StatusCode, body)
	}
	var taggedX knowledge.Doc
	_ = json.Unmarshal(body, &taggedX)
	if len(taggedX.Tags) != 2 {
		t.Fatalf("expected 2 tags on Doc X, got %+v", taggedX.Tags)
	}

	resp, _ = rig.doJSON(t, "PUT", "/api/v1/documents/"+docY.ID+"/tags", tokenA, map[string][]string{
		"tag_ids": {tag1.ID},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatal(resp.StatusCode)
	}

	resp, body = rig.doJSON(t, "GET", "/api/v1/documents?tag_id="+tag1.ID, tokenA, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatal(resp.StatusCode)
	}
	var tagFilterResp struct {
		Items []knowledge.Doc `json:"items"`
		Total int             `json:"total"`
	}
	_ = json.Unmarshal(body, &tagFilterResp)
	if tagFilterResp.Total != 2 || len(tagFilterResp.Items) != 2 {
		t.Fatalf("AC-05-03 failed: expected 2 docs (X, Y) under tag1, got %d", tagFilterResp.Total)
	}
	for _, item := range tagFilterResp.Items {
		if item.ID != docX.ID && item.ID != docY.ID {
			t.Fatalf("unexpected doc under tag1: %s", item.Title)
		}
	}

	resp, _ = rig.doJSON(t, "DELETE", "/api/v1/tags/"+tag1.ID, tokenA, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("AC-05-04 delete tag failed: %d", resp.StatusCode)
	}
	resp, body = rig.doJSON(t, "GET", "/api/v1/documents/"+docX.ID, tokenA, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("doc X should still exist after tag delete: %d", resp.StatusCode)
	}
	var docXAfter knowledge.Doc
	_ = json.Unmarshal(body, &docXAfter)
	if len(docXAfter.Tags) != 1 || docXAfter.Tags[0].ID != tag2.ID {
		t.Fatalf("Doc X should retain remaining tag: %+v", docXAfter.Tags)
	}

	resp, _ = rig.doJSON(t, "PATCH", "/api/v1/tags/"+tag2.ID, tokenB, map[string]string{"name": "Hacked"})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("AC-05-05: expected 404 for foreign tag access, got %d", resp.StatusCode)
	}
	resp, _ = rig.doJSON(t, "DELETE", "/api/v1/tags/"+tag2.ID, tokenB, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("AC-05-05: expected 404 for foreign tag delete, got %d", resp.StatusCode)
	}
}

func TestAdminIsolationAndGates(t *testing.T) {
	rig := newTestRig(false)
	_, userToken := rig.registerUser(t, "normal@example.com", "Normal User", "Pass123456", true)
	_, adminToken := rig.registerAdmin(t, "admin@example.com", "Admin User", "Admin123456")

	resp, body := rig.doJSON(t, "POST", "/api/v1/kbs", userToken, map[string]string{"name": "User KB"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatal(resp.StatusCode)
	}
	var kb knowledge.KB
	_ = json.Unmarshal(body, &kb)

	pdf := makeSamplePDF(5)
	uploadBody, ct := buildUploadMultipart("doc.pdf", pdf, map[string]string{"title": "User Paper"})
	resp, body = rig.doReq(t, "POST", "/api/v1/kbs/"+kb.ID+"/documents", userToken, uploadBody, ct)
	if resp.StatusCode != http.StatusCreated {
		t.Fatal(resp.StatusCode)
	}
	var doc knowledge.Doc
	_ = json.Unmarshal(body, &doc)

	resp, body = rig.doJSON(t, "POST", "/api/v1/tags", userToken, map[string]string{"name": "User Tag"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatal(resp.StatusCode)
	}
	var tag knowledge.Tag
	_ = json.Unmarshal(body, &tag)

	resp, body = rig.doJSON(t, "GET", "/api/v1/kbs", adminToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("admin GET /kbs expected 200, got %d", resp.StatusCode)
	}
	var adminKBs []knowledge.KB
	_ = json.Unmarshal(body, &adminKBs)
	if len(adminKBs) != 0 {
		t.Fatalf("AC-08-05: admin GET /kbs should be empty list, got %+v", adminKBs)
	}

	resp, body = rig.doJSON(t, "GET", "/api/v1/documents", adminToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("admin GET /documents expected 200, got %d", resp.StatusCode)
	}
	var adminDocs struct {
		Items []knowledge.Doc `json:"items"`
		Total int             `json:"total"`
	}
	_ = json.Unmarshal(body, &adminDocs)
	if adminDocs.Total != 0 || len(adminDocs.Items) != 0 {
		t.Fatalf("AC-08-05: admin GET /documents should be empty list, got %+v", adminDocs)
	}

	resp, body = rig.doJSON(t, "GET", "/api/v1/tags", adminToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("admin GET /tags expected 200, got %d", resp.StatusCode)
	}
	var adminTags []knowledge.Tag
	_ = json.Unmarshal(body, &adminTags)
	if len(adminTags) != 0 {
		t.Fatalf("AC-08-05: admin GET /tags should be empty list, got %+v", adminTags)
	}

	resp, _ = rig.doJSON(t, "POST", "/api/v1/kbs", adminToken, map[string]string{"name": "Admin KB"})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("admin POST /kbs should be 403 Forbidden, got %d", resp.StatusCode)
	}

	resp, _ = rig.doJSON(t, "POST", "/api/v1/tags", adminToken, map[string]string{"name": "Admin Tag"})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("admin POST /tags should be 403 Forbidden, got %d", resp.StatusCode)
	}

	resp, body = rig.doJSON(t, "GET", "/api/v1/kbs/"+kb.ID, adminToken, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("admin GET /kbs/{id} should be 404, got %d", resp.StatusCode)
	}
	if strings.Contains(string(body), "User KB") {
		t.Fatal("404 response leaked KB name")
	}

	resp, body = rig.doJSON(t, "GET", "/api/v1/documents/"+doc.ID, adminToken, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("admin GET /documents/{id} should be 404, got %d", resp.StatusCode)
	}
	if strings.Contains(string(body), "User Paper") {
		t.Fatal("404 response leaked doc title")
	}

	resp, body = rig.doReq(t, "GET", "/api/v1/documents/"+doc.ID+"/file", adminToken, nil, "")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("admin download should be 404, got %d", resp.StatusCode)
	}
	if len(body) > 500 || strings.Contains(string(body), "PDF") {
		t.Fatal("admin download leaked file content")
	}

	resp, _ = rig.doReq(t, "POST", "/api/v1/kbs/"+kb.ID+"/documents", adminToken, uploadBody, ct)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("admin upload should be 404, got %d", resp.StatusCode)
	}

	resp, _ = rig.doJSON(t, "PATCH", "/api/v1/tags/"+tag.ID, adminToken, map[string]string{"name": "Admin Tag"})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("admin patch tag should be 404, got %d", resp.StatusCode)
	}

	resp, _ = rig.doReq(t, "GET", "/files/user/doc.pdf", userToken, nil, "")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("AC-08-04: direct storage path request must 404, got %d", resp.StatusCode)
	}

	resp, _ = rig.doJSON(t, "GET", "/api/v1/documents", "", nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("AC-08-03: unauthenticated request must 401, got %d", resp.StatusCode)
	}
}

func TestEmailRestrictionGate(t *testing.T) {
	rig := newTestRig(true)
	_, unverifiedToken := rig.registerUser(t, "unverified@example.com", "Unverified", "Pass123456", false)
	_, verifiedToken := rig.registerUser(t, "verified@example.com", "Verified", "Pass123456", true)

	resp, body := rig.doJSON(t, "POST", "/api/v1/kbs", verifiedToken, map[string]string{"name": "Verified KB"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatal(resp.StatusCode)
	}
	var kb knowledge.KB
	_ = json.Unmarshal(body, &kb)

	pdf := makeSamplePDF(3)
	uploadBody, ct := buildUploadMultipart("doc.pdf", pdf, map[string]string{"title": "Paper"})
	resp, body = rig.doReq(t, "POST", "/api/v1/kbs/"+kb.ID+"/documents", unverifiedToken, uploadBody, ct)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 EMAIL_UNVERIFIED for unverified user upload when restricted, got %d: %s", resp.StatusCode, body)
	}
	var errResp errorBody
	_ = json.Unmarshal(body, &errResp)
	if errResp.Code != "EMAIL_UNVERIFIED" {
		t.Fatalf("expected EMAIL_UNVERIFIED code, got %s", errResp.Code)
	}

	resp, _ = rig.doReq(t, "POST", "/api/v1/kbs/"+kb.ID+"/documents", verifiedToken, uploadBody, ct)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("verified user upload should succeed: %d", resp.StatusCode)
	}
}
