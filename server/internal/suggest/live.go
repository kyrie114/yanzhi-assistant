package suggest

import "context"

// Catalog loads the caller's own finished texts. A missing library or document is ErrNotFound.
type Catalog interface {
	Document(ctx context.Context, owner, id string) (SourceDoc, error)
	Library(ctx context.Context, owner, id string) ([]SourceDoc, error)
}

type Live struct {
	Memory  *Memory
	Catalog Catalog
}

func (l Live) List(ctx context.Context, caller, scopeType, scopeID, asked string) (Result, error) {
	if l.Memory == nil || l.Catalog == nil {
		return Result{}, ErrNotFound
	}
	switch scopeType {
	case "document":
		doc, err := l.Catalog.Document(ctx, caller, scopeID)
		if err != nil {
			return Result{}, err
		}
		if !doc.Ready {
			l.Memory.PutDocument(scopeID, caller, false, nil)
			return l.Memory.List(caller, "document", scopeID, asked)
		}
		if !l.Memory.documentCurrent(scopeID, caller, doc.Version) {
			l.Memory.GenerateDocument(scopeID, caller, true, doc.Texts, l.Memory.modelOrNil())
			l.Memory.markDocumentVersion(scopeID, caller, doc.Version)
		}
		return l.Memory.List(caller, "document", scopeID, asked)
	case "kb":
		docs, err := l.Catalog.Library(ctx, caller, scopeID)
		if err != nil {
			return Result{}, err
		}
		l.Memory.SetKBDocuments(scopeID, caller, docs)
		return l.Memory.List(caller, "kb", scopeID, asked)
	default:
		return Result{}, ErrNotFound
	}
}

func (m *Memory) modelOrNil() QuestionGenerator {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.model
}
