package integration

import (
	"bytes"
	"context"
	"testing"

	"yanzhi/server/internal/knowledge"
	"yanzhi/server/internal/platform/db"
)

func TestIntegrationKnowledgeUploadDuplicateAndDelete(t *testing.T) {
	pool := requireDB(t)
	svc := newKnowledge(t)
	ctx := context.Background()
	user := createUser(t, "knowledge")
	kb := createKB(t, svc, user.ID)
	data := tinyPDF(1)

	meta := knowledge.UploadMeta{
		FileName: "paper.pdf", Title: "图神经网络综述", Authors: []string{"张三"},
		IdempotencyKey: "idem-" + db.NewID(), RequestID: db.NewID(),
	}
	result, err := svc.Upload(ctx, user.ID, kb.ID, meta, data)
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if result.Status != 201 {
		t.Fatalf("expected 201, got %d", result.Status)
	}
	if result.Doc.Status != "待处理" || result.Doc.Title != "图神经网络综述" {
		t.Fatalf("unexpected document: %+v", result.Doc)
	}

	var kind string
	if err := pool.QueryRow(ctx, `SELECT kind FROM jobs WHERE target_id = $1`, db.MustUUID(result.Doc.ID)).Scan(&kind); err != nil {
		t.Fatalf("ingest job not queued: %v", err)
	}
	if kind != "ingest" {
		t.Fatalf("expected ingest job, got %q", kind)
	}

	// 同一份字节再传一次：返回已有记录并拒绝新建。
	if _, err := svc.Upload(ctx, user.ID, kb.ID, knowledge.UploadMeta{
		FileName: "paper.pdf", Title: "另一个标题", RequestID: db.NewID(),
	}, data); codeOf(err) != "DUPLICATE_DOCUMENT" {
		t.Fatalf("duplicate upload: %v", err)
	}

	// 同一幂等键同一内容：复用原来那次 201 的结果。
	replay, err := svc.Upload(ctx, user.ID, kb.ID, meta, data)
	if err != nil {
		t.Fatalf("idempotent replay: %v", err)
	}
	if replay.Status != 201 || replay.Doc.ID != result.Doc.ID {
		t.Fatalf("replay should reuse document, got %d %s", replay.Status, replay.Doc.ID)
	}

	items, total, err := svc.ListDocs(ctx, user.ID, kb.ID, "", nil, 1, 20)
	if err != nil {
		t.Fatalf("ListDocs: %v", err)
	}
	if total != 1 || len(items) != 1 {
		t.Fatalf("expected one document, got total=%d items=%d", total, len(items))
	}

	title, got, err := svc.File(ctx, user.ID, result.Doc.ID)
	if err != nil {
		t.Fatalf("File: %v", err)
	}
	if title != "图神经网络综述" || !bytes.Equal(got, data) {
		t.Fatalf("file round trip mismatch: %q %d bytes", title, len(got))
	}

	// 其他用户看不到这份资料。
	other := createUser(t, "knowledge-other")
	if _, err := svc.GetDoc(ctx, other.ID, result.Doc.ID); codeOf(err) != "NOT_FOUND" {
		t.Fatalf("other owner should get 404, got %v", err)
	}

	if err := svc.Delete(ctx, user.ID, result.Doc.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := svc.GetDoc(ctx, user.ID, result.Doc.ID); codeOf(err) != "NOT_FOUND" {
		t.Fatalf("deleted document should be gone, got %v", err)
	}
}
