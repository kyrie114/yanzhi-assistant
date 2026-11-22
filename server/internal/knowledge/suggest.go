package knowledge

import (
	"context"
	"strconv"
	"time"

	"errors"

	"github.com/jackc/pgx/v5"

	"yanzhi/server/internal/apperr"
	"yanzhi/server/internal/platform/db"
	"yanzhi/server/internal/suggest"
)

func (s Service) SuggestionDocument(ctx context.Context, owner, id string) (suggest.SourceDoc, error) {
	var status string
	var updated time.Time
	var title string
	err := s.Pool.QueryRow(ctx, `
		SELECT title, status, updated_at FROM documents WHERE id = $1 AND owner_id = $2`,
		db.MustUUID(id), db.MustUUID(owner)).Scan(&title, &status, &updated)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return suggest.SourceDoc{}, suggest.ErrNotFound
		}
		return suggest.SourceDoc{}, err
	}
	doc := suggest.SourceDoc{ID: id, Ready: status == "ready"}
	texts, n, err := s.suggestionTexts(ctx, id, title)
	if err != nil {
		return suggest.SourceDoc{}, err
	}
	doc.Texts = texts
	doc.Version = updated.UTC().Format(time.RFC3339Nano) + ":" + strconv.Itoa(n)
	return doc, nil
}

func (s Service) SuggestionLibrary(ctx context.Context, owner, kbID string) ([]suggest.SourceDoc, error) {
	if _, err := s.GetKB(ctx, owner, kbID); err != nil {
		if errors.Is(err, apperr.ErrNotFound) {
			return nil, suggest.ErrNotFound
		}
		return nil, err
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT id::text FROM documents
		WHERE owner_id = $1 AND kb_id = $2 AND status = 'ready'
		ORDER BY updated_at DESC LIMIT 5`, db.MustUUID(owner), db.MustUUID(kbID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	docs := make([]suggest.SourceDoc, 0, len(ids))
	for _, id := range ids {
		doc, err := s.SuggestionDocument(ctx, owner, id)
		if err != nil {
			return nil, err
		}
		docs = append(docs, doc)
	}
	return docs, nil
}

func (s Service) suggestionTexts(ctx context.Context, docID, title string) ([]string, int, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT text FROM chunks WHERE document_id = $1 ORDER BY paragraph_index LIMIT 8`,
		db.MustUUID(docID))
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	texts := make([]string, 0, 8)
	for rows.Next() {
		var text string
		if err := rows.Scan(&text); err != nil {
			return nil, 0, err
		}
		texts = append(texts, text)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	if len(texts) == 0 && title != "" {
		texts = []string{title}
	}
	return texts, len(texts), nil
}
