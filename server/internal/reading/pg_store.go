package reading

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"yanzhi/server/internal/apperr"
	"yanzhi/server/internal/platform/db"
)

// PGStore 基于 PostgreSQL 实现按页阅读数据源与 page_translations 持久化缓存。
type PGStore struct {
	pool *pgxpool.Pool
}

// NewPGStore 创建 PostgreSQL 按页阅读存储。
func NewPGStore(pool *pgxpool.Pool) *PGStore {
	return &PGStore{pool: pool}
}

func (s *PGStore) GetDocMeta(ctx context.Context, owner, docID string) (DocMeta, error) {
	ownerUUID, okOwner := db.UUID(owner)
	docUUID, okDoc := db.UUID(docID)
	if !okOwner || !okDoc {
		return DocMeta{}, apperr.ErrNotFound
	}
	var out DocMeta
	var storageKey string
	err := s.pool.QueryRow(ctx, `
		SELECT id::text, owner_id::text, title, source_type, status,
		       COALESCE(page_count, 0), has_physical_pages, COALESCE(storage_key, '')
		FROM documents
		WHERE id = $1 AND owner_id = $2`,
		docUUID, ownerUUID).Scan(
		&out.ID, &out.OwnerID, &out.Title, &out.SourceType, &out.Status,
		&out.PageCount, &out.HasPhysicalPages, &storageKey,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return DocMeta{}, apperr.ErrNotFound
	}
	if err != nil {
		return DocMeta{}, err
	}
	out.HasOriginalFile = strings.EqualFold(out.SourceType, "pdf") && storageKey != ""
	return out, nil
}

func (s *PGStore) ListPageChunks(ctx context.Context, owner, docID string, page int) ([]PageChunk, error) {
	ownerUUID, okOwner := db.UUID(owner)
	docUUID, okDoc := db.UUID(docID)
	if !okOwner || !okDoc {
		return nil, apperr.ErrNotFound
	}
	rows, err := s.pool.Query(ctx, `
		SELECT c.text, c.overlap_prefix
		FROM chunks c
		JOIN documents d ON d.id = c.document_id
		WHERE d.id = $1 AND d.owner_id = $2 AND c.page_start = $3
		ORDER BY c.paragraph_index ASC, c.id ASC`,
		docUUID, ownerUUID, page)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var chunks []PageChunk
	for rows.Next() {
		var ch PageChunk
		if err := rows.Scan(&ch.Text, &ch.OverlapPrefix); err != nil {
			return nil, err
		}
		chunks = append(chunks, ch)
	}
	return chunks, rows.Err()
}

func (s *PGStore) GetTranslation(ctx context.Context, owner, docID string, page int, sourceSHA256 string) (string, bool, error) {
	ownerUUID, okOwner := db.UUID(owner)
	docUUID, okDoc := db.UUID(docID)
	if !okOwner || !okDoc {
		return "", false, nil
	}
	var idText, storedHash, translation string
	err := s.pool.QueryRow(ctx, `
		SELECT id::text, source_sha256, translation
		FROM page_translations
		WHERE document_id = $1 AND owner_id = $2 AND page_no = $3`,
		docUUID, ownerUUID, page).Scan(&idText, &storedHash, &translation)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if storedHash != sourceSHA256 {
		// 切块修订使该页源文本哈希变化时清除过期译文行，确保后续不再命中旧缓存。
		if rowUUID, ok := db.UUID(idText); ok {
			if _, delErr := s.pool.Exec(ctx, `DELETE FROM page_translations WHERE id = $1`, rowUUID); delErr != nil {
				return "", false, delErr
			}
		}
		return "", false, nil
	}
	return translation, true, nil
}

func (s *PGStore) SaveTranslation(ctx context.Context, owner, docID string, page int, sourceSHA256, translation string) error {
	ownerUUID, okOwner := db.UUID(owner)
	docUUID, okDoc := db.UUID(docID)
	if !okOwner || !okDoc {
		return apperr.ErrNotFound
	}
	rowUUID := db.MustUUID(uuid.NewString())
	_, err := s.pool.Exec(ctx, `
		INSERT INTO page_translations (id, document_id, owner_id, page_no, source_sha256, translation, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, now())
		ON CONFLICT (document_id, page_no)
		DO UPDATE SET source_sha256 = EXCLUDED.source_sha256,
		              translation = EXCLUDED.translation,
		              created_at = EXCLUDED.created_at
		WHERE page_translations.owner_id = EXCLUDED.owner_id`,
		rowUUID, docUUID, ownerUUID, page, sourceSHA256, translation)
	return err
}
