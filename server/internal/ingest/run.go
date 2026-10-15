package ingest

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pgvector/pgvector-go"
	"yanzhi/server/internal/apperr"
	"yanzhi/server/internal/jobs"
	"yanzhi/server/internal/platform/db"
	"yanzhi/server/internal/platform/docreader"
	"yanzhi/server/internal/platform/files"
	"yanzhi/server/internal/provider"
	"yanzhi/server/internal/textutil"
)

type Runner struct {
	Pool      *pgxpool.Pool
	Files     files.Store
	Reader    *docreader.Client
	Models    provider.Store
	AI        provider.Client
	Chunk     Config
	EmbedBatch int
	Timeout   time.Duration
}

func (r Runner) Run(ctx context.Context, job jobs.Job) error {
	return r.ingest(ctx, job, false)
}

func (r Runner) Reembed(ctx context.Context, job jobs.Job) error {
	return r.ingest(ctx, job, true)
}

func (r Runner) ingest(ctx context.Context, job jobs.Job, reembed bool) error {
	if reembed {
		return r.reembed(ctx, job)
	}
	_ = jobs.Stage(ctx, r.Pool, job.ID, "fetch", "running")
	var key string
	err := r.Pool.QueryRow(ctx, `SELECT COALESCE(storage_key, '') FROM documents WHERE id = $1`, db.MustUUID(job.TargetID)).Scan(&key)
	if err != nil {
		return r.fail(ctx, job, apperr.MsgCorrupted)
	}
	data, err := r.Files.Get(key)
	if err != nil {
		_ = jobs.Stage(ctx, r.Pool, job.ID, "fetch", "failed")
		return r.fail(ctx, job, apperr.MsgCorrupted)
	}
	_ = jobs.Stage(ctx, r.Pool, job.ID, "fetch", "succeeded")
	if ok, err := Alive(ctx, r.Pool, job.TargetID); err != nil || !ok {
		return jobs.ErrCancelled
	}
	_ = jobs.Stage(ctx, r.Pool, job.ID, "parse", "running")
	parsed, err := r.Reader.ParsePDF(ctx, job.TargetID, job.RequestID, data)
	if err != nil {
		_ = jobs.Stage(ctx, r.Pool, job.ID, "parse", "failed")
		return r.fail(ctx, job, apperr.MsgDocreader)
	}
	if parsed.ErrCode != "" {
		_ = jobs.Stage(ctx, r.Pool, job.ID, "parse", "failed")
		return r.fail(ctx, job, reasonForParse(parsed.ErrCode))
	}
	_ = jobs.Stage(ctx, r.Pool, job.ID, "parse", "succeeded")
	_ = jobs.Stage(ctx, r.Pool, job.ID, "recognize", "running")
	if !hasText(parsed.Pages) {
		_ = jobs.Stage(ctx, r.Pool, job.ID, "recognize", "failed")
		return r.fail(ctx, job, apperr.MsgRecognize)
	}
	_ = jobs.Stage(ctx, r.Pool, job.ID, "recognize", "succeeded")
	if ok, err := Alive(ctx, r.Pool, job.TargetID); err != nil || !ok {
		return jobs.ErrCancelled
	}
	_ = jobs.Stage(ctx, r.Pool, job.ID, "chunk", "running")
	chunks, parents := Pack(parsed.Pages, r.Chunk)
	if len(chunks) == 0 {
		_ = jobs.Stage(ctx, r.Pool, job.ID, "chunk", "failed")
		return r.fail(ctx, job, apperr.MsgRecognize)
	}
	gen, err := r.Models.ActiveGeneration(ctx)
	if err != nil {
		return r.fail(ctx, job, apperr.ErrModelMissing.Message)
	}
	if err := r.storeChunks(ctx, job, parsed, chunks, parents, gen); err != nil {
		if errors.Is(err, jobs.ErrCancelled) {
			return err
		}
		_ = jobs.Stage(ctx, r.Pool, job.ID, "chunk", "failed")
		_ = r.cleanup(ctx, job.OwnerID, job.TargetID)
		return r.fail(ctx, job, "切块写入失败")
	}
	_ = jobs.Stage(ctx, r.Pool, job.ID, "chunk", "succeeded")
	_ = jobs.Stage(ctx, r.Pool, job.ID, "embed", "running")
	if err := r.embedDocument(ctx, job, false); err != nil {
		return err
	}
	_ = jobs.Stage(ctx, r.Pool, job.ID, "embed", "succeeded")
	return r.finishReady(ctx, job)
}

func (r Runner) reembed(ctx context.Context, job jobs.Job) error {
	_ = jobs.Stage(ctx, r.Pool, job.ID, "fetch", "skipped")
	_ = jobs.Stage(ctx, r.Pool, job.ID, "parse", "skipped")
	_ = jobs.Stage(ctx, r.Pool, job.ID, "recognize", "skipped")
	_ = jobs.Stage(ctx, r.Pool, job.ID, "chunk", "skipped")
	_ = jobs.Stage(ctx, r.Pool, job.ID, "embed", "running")
	if err := r.embedDocument(ctx, job, true); err != nil {
		return err
	}
	_ = jobs.Stage(ctx, r.Pool, job.ID, "embed", "succeeded")
	return r.finishReady(ctx, job)
}

func (r Runner) storeChunks(ctx context.Context, job jobs.Job, parsed docreader.Result, chunks []ChunkDraft, parents []ParentDraft, gen int64) error {
	tx, err := r.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var cancel bool
	err = tx.QueryRow(ctx, `SELECT cancel_requested FROM documents WHERE id = $1 FOR UPDATE`, db.MustUUID(job.TargetID)).Scan(&cancel)
	if err != nil || cancel {
		return jobs.ErrCancelled
	}
	if err := DeleteDerived(ctx, tx, job.OwnerID, job.TargetID); err != nil {
		return err
	}
	parentIDs := make([]string, len(parents))
	for i, p := range parents {
		parentIDs[i] = db.NewID()
		if _, err := tx.Exec(ctx, `
			INSERT INTO parent_chunks (id, document_id, position, page_start, text)
			VALUES ($1, $2, $3, $4, $5)`,
			db.MustUUID(parentIDs[i]), db.MustUUID(job.TargetID), p.Position, p.PageStart, p.Text); err != nil {
			return err
		}
	}
	ids := make([]string, len(chunks))
	now := time.Now().UTC()
	for i, ch := range chunks {
		ids[i] = db.NewID()
		terms := textutil.Terms(ch.Text)
		parent := parentIDs[0]
		if ch.ParentIndex >= 0 && ch.ParentIndex < len(parentIDs) {
			parent = parentIDs[ch.ParentIndex]
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO chunks (
				id, document_id, parent_id, kind, page_start, paragraph_index, overlap_prefix,
				text, version_no, char_count, term_count, embedding_generation, created_at, updated_at
			) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,1,$9,$10,$11,$12,$12)`,
			db.MustUUID(ids[i]), db.MustUUID(job.TargetID), db.MustUUID(parent), ch.Kind, ch.PageStart,
			ch.ParagraphIndex, ch.OverlapPrefix, ch.Text, textutil.RuneCount(ch.Text), len(terms), gen, now); err != nil {
			return err
		}
		tf := map[string]int{}
		for _, term := range terms {
			tf[term]++
		}
		for term, n := range tf {
			if _, err := tx.Exec(ctx, `
				INSERT INTO chunk_postings (chunk_id, term, field, tf) VALUES ($1, $2, 'text', $3)`,
				db.MustUUID(ids[i]), term, n); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO term_stats (owner_id, term, df) VALUES ($1, $2, 1)
				ON CONFLICT (owner_id, term) DO UPDATE SET df = term_stats.df + 1`,
				db.MustUUID(job.OwnerID), term); err != nil {
				return err
			}
		}
	}
	for i := range ids {
		var prev, next any
		if i > 0 {
			prev = db.MustUUID(ids[i-1])
		}
		if i+1 < len(ids) {
			next = db.MustUUID(ids[i+1])
		}
		if _, err := tx.Exec(ctx, `UPDATE chunks SET prev_chunk_id = $2, next_chunk_id = $3 WHERE id = $1`, db.MustUUID(ids[i]), prev, next); err != nil {
			return err
		}
	}
	skipped := parsed.Skipped
	if skipped == nil {
		skipped = []int{}
	}
	pages32 := make([]int32, len(skipped))
	for i, n := range skipped {
		pages32[i] = int32(n)
	}
	tag, err := tx.Exec(ctx, `
		UPDATE documents
		SET page_count = $2, has_physical_pages = $3, skipped_pages = $4, embedding_generation = $5,
		    status = 'embedding', updated_at = now()
		WHERE id = $1 AND cancel_requested = false`,
		db.MustUUID(job.TargetID), parsed.PageCount, parsed.HasPhysicalPages, pages32, gen)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return jobs.ErrCancelled
	}
	return tx.Commit(ctx)
}

func (r Runner) embedDocument(ctx context.Context, job jobs.Job, keepText bool) error {
	remote, err := r.Models.Load(ctx, "embedding")
	if err != nil {
		if keepText {
			return r.failKeep(ctx, job, apperr.ErrModelMissing.Message)
		}
		_ = r.cleanup(ctx, job.OwnerID, job.TargetID)
		return r.fail(ctx, job, apperr.ErrModelMissing.Message)
	}
	rows, err := r.Pool.Query(ctx, `
		SELECT id::text, text FROM chunks WHERE document_id = $1 ORDER BY paragraph_index`, db.MustUUID(job.TargetID))
	if err != nil {
		return err
	}
	type row struct{ id, text string }
	var chunks []row
	for rows.Next() {
		var item row
		if err := rows.Scan(&item.id, &item.text); err != nil {
			rows.Close()
			return err
		}
		chunks = append(chunks, item)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(chunks) == 0 {
		if keepText {
			return r.failKeep(ctx, job, apperr.MsgRecognize)
		}
		return r.fail(ctx, job, apperr.MsgRecognize)
	}
	batch := r.EmbedBatch
	if batch < 1 {
		batch = 16
	}
	gen, err := r.Models.ActiveGeneration(ctx)
	if err != nil {
		return err
	}
	for start := 0; start < len(chunks); start += batch {
		if ok, err := Alive(ctx, r.Pool, job.TargetID); err != nil || !ok {
			return jobs.ErrCancelled
		}
		jobs.Heartbeat(ctx, r.Pool, job.ID)
		end := start + batch
		if end > len(chunks) {
			end = len(chunks)
		}
		part := chunks[start:end]
		texts := make([]string, len(part))
		for i := range part {
			texts[i] = part[i].text
		}
		vecs, err := r.embedRetry(ctx, remote, texts)
		if err != nil {
			if keepText {
				_, _ = r.Pool.Exec(ctx, `UPDATE chunks SET embedding = NULL WHERE document_id = $1`, db.MustUUID(job.TargetID))
				return r.failKeep(ctx, job, apperr.MsgEmbedDown)
			}
			_ = r.cleanup(ctx, job.OwnerID, job.TargetID)
			return r.fail(ctx, job, apperr.MsgEmbedDown)
		}
		if remote.Dimension > 0 && len(vecs[0]) != remote.Dimension {
			if keepText {
				_, _ = r.Pool.Exec(ctx, `UPDATE chunks SET embedding = NULL WHERE document_id = $1`, db.MustUUID(job.TargetID))
				return r.failKeep(ctx, job, "向量维度与配置不一致")
			}
			_ = r.cleanup(ctx, job.OwnerID, job.TargetID)
			return r.fail(ctx, job, "向量维度与配置不一致")
		}
		for i, vec := range vecs {
			tag, err := r.Pool.Exec(ctx, `
				UPDATE chunks SET embedding = $2, embedding_generation = $3, updated_at = now()
				WHERE id = $1 AND document_id = $4`,
				db.MustUUID(part[i].id), pgvector.NewVector(vec), gen, db.MustUUID(job.TargetID))
			if err != nil {
				if keepText {
					_, _ = r.Pool.Exec(ctx, `UPDATE chunks SET embedding = NULL WHERE document_id = $1`, db.MustUUID(job.TargetID))
					return r.failKeep(ctx, job, apperr.MsgEmbedDown)
				}
				_ = r.cleanup(ctx, job.OwnerID, job.TargetID)
				return r.fail(ctx, job, apperr.MsgEmbedDown)
			}
			if tag.RowsAffected() == 0 {
				return jobs.ErrCancelled
			}
		}
	}
	return nil
}

func (r Runner) embedRetry(ctx context.Context, remote provider.Remote, texts []string) ([][]float32, error) {
	var last error
	for attempt := 0; attempt < 3; attempt++ {
		callCtx, cancel := context.WithTimeout(ctx, r.timeout())
		vecs, _, err := r.AI.Embed(callCtx, remote, texts)
		cancel()
		if err == nil {
			return vecs, nil
		}
		last = err
		if attempt < 2 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(attempt+1) * time.Second):
			}
		}
	}
	return nil, last
}

func (r Runner) finishReady(ctx context.Context, job jobs.Job) error {
	if ok, err := Alive(ctx, r.Pool, job.TargetID); err != nil || !ok {
		return jobs.ErrCancelled
	}
	var enabled bool
	err := r.Pool.QueryRow(ctx, `
		SELECT k.graph_enabled FROM knowledge_bases k
		JOIN documents d ON d.kb_id = k.id
		WHERE d.id = $1`, db.MustUUID(job.TargetID)).Scan(&enabled)
	if err != nil {
		return err
	}
	var graphStatus *string
	if enabled {
		ready := "pending"
		graphStatus = &ready
	}
	tag, err := r.Pool.Exec(ctx, `
		UPDATE documents
		SET status = 'ready', failure_reason = NULL, graph_status = $2, updated_at = now()
		WHERE id = $1 AND cancel_requested = false`, db.MustUUID(job.TargetID), graphStatus)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return jobs.ErrCancelled
	}
	if enabled {
		_ = jobs.Enqueue(ctx, r.Pool, "graph_extract", job.OwnerID, job.TargetID, job.RequestID, 80, 1)
	}
	return nil
}

func (r Runner) fail(ctx context.Context, job jobs.Job, reason string) error {
	_, err := r.Pool.Exec(ctx, `
		UPDATE documents SET status = 'failed', failure_reason = $2, updated_at = now()
		WHERE id = $1 AND cancel_requested = false`, db.MustUUID(job.TargetID), reason)
	if err != nil {
		return err
	}
	return errors.New(reason)
}

func (r Runner) failKeep(ctx context.Context, job jobs.Job, reason string) error {
	_, err := r.Pool.Exec(ctx, `
		UPDATE documents SET status = 'failed', failure_reason = $2, updated_at = now()
		WHERE id = $1 AND cancel_requested = false`, db.MustUUID(job.TargetID), reason)
	if err != nil {
		return err
	}
	return errors.New(reason)
}

func (r Runner) cleanup(ctx context.Context, owner, documentID string) error {
	tx, err := r.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := DeleteDerived(ctx, tx, owner, documentID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (r Runner) timeout() time.Duration {
	if r.Timeout <= 0 {
		return 60 * time.Second
	}
	return r.Timeout
}

func hasText(pages []Page) bool {
	for _, p := range pages {
		for _, b := range p.Blocks {
			if textutil.RuneCount(trimSpace(b.Text)) > 0 {
				return true
			}
		}
	}
	return false
}

func reasonForParse(code string) string {
	switch code {
	case "ENCRYPTED":
		return apperr.MsgEncrypted
	case "TOO_MANY_PAGES":
		return apperr.ErrTooManyPages.Message
	default:
		return apperr.MsgCorrupted
	}
}
