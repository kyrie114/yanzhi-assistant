package graph

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"yanzhi/server/internal/apperr"
	"yanzhi/server/internal/platform/db"
)

func TestNeighborsOneHop(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL is required")
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

	owner := uuid.NewString()
	other := uuid.NewString()
	kbID := uuid.NewString()
	otherKB := uuid.NewString()
	docID := uuid.NewString()
	foreignID := uuid.NewString()
	paperID := uuid.NewString()
	neighborID := uuid.NewString()
	secondID := uuid.NewString()
	paperEdge := uuid.NewString()
	secondEdge := uuid.NewString()
	between := uuid.NewString()
	foreignPaper := uuid.NewString()
	const foreignTitle = "secret-foreign-title"
	const foreignName = "secret-foreign-paper"

	t.Cleanup(func() {
		ids, ok := db.UUIDs([]string{owner, other})
		if !ok {
			return
		}
		bg := context.Background()
		_, _ = pool.Exec(bg, `DELETE FROM kg_relations WHERE owner_id = ANY($1::uuid[])`, ids)
		_, _ = pool.Exec(bg, `DELETE FROM kg_entities WHERE owner_id = ANY($1::uuid[])`, ids)
		_, _ = pool.Exec(bg, `DELETE FROM documents WHERE owner_id = ANY($1::uuid[])`, ids)
		_, _ = pool.Exec(bg, `DELETE FROM knowledge_bases WHERE owner_id = ANY($1::uuid[])`, ids)
		_, _ = pool.Exec(bg, `DELETE FROM users WHERE id = ANY($1::uuid[])`, ids)
	})

	mustExec(t, pool, `
		INSERT INTO users (id, email, name, password_hash, role, created_at)
		VALUES ($1, $2, 'owner', 'x', 'user', now()), ($3, $4, 'other', 'x', 'user', now())`,
		owner, owner+"@example.edu.cn", other, other+"@example.edu.cn")
	mustExec(t, pool, `
		INSERT INTO knowledge_bases (id, owner_id, name, created_at, updated_at)
		VALUES ($1, $2, 'kb', now(), now()), ($3, $4, 'other', now(), now())`,
		kbID, owner, otherKB, other)
	mustExec(t, pool, `
		INSERT INTO documents (id, owner_id, kb_id, source_type, title, status, created_at, updated_at)
		VALUES ($1, $2, $3, 'pdf', 'owned-doc', 'ready', now(), now()),
		       ($4, $5, $6, 'pdf', $7, 'ready', now(), now())`,
		docID, owner, kbID, foreignID, other, otherKB, foreignTitle)
	mustExec(t, pool, `
		INSERT INTO kg_entities (id, owner_id, type, name, merge_key, document_id, created_at)
		VALUES ($1, $2, 'paper', 'owned-paper', $3, $4, now()),
		       ($5, $6, 'paper', $7, $8, $9, now())`,
		paperID, owner, PaperKey(docID), docID,
		foreignPaper, other, foreignName, PaperKey(foreignID), foreignID)
	mustExec(t, pool, `
		INSERT INTO kg_entities (id, owner_id, type, name, merge_key, created_at)
		VALUES ($1, $2, 'method', 'neighbor-one', $3, now()),
		       ($4, $2, 'method', 'neighbor-two', $5, now())`,
		neighborID, owner, "method:neighbor-one", secondID, "method:neighbor-two")
	mustExec(t, pool, `
		INSERT INTO kg_relations (id, owner_id, type, source_id, target_id, suspended, created_at)
		VALUES ($1, $2, 'proposes', $3, $4, false, now()),
		       ($5, $2, 'proposes', $3, $6, false, now()),
		       ($7, $2, 'compares_with', $4, $6, false, now())`,
		paperEdge, owner, paperID, neighborID, secondEdge, secondID, between)

	svc := Service{Pool: pool}
	view, err := svc.Neighbors(ctx, owner, docID)
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Nodes) != 3 || view.Nodes[0].ID != paperID || view.Nodes[0].Name != "owned-paper" {
		t.Fatalf("nodes %+v", view.Nodes)
	}
	gotNodes := map[string]bool{}
	for _, n := range view.Nodes {
		gotNodes[n.ID] = true
	}
	if !gotNodes[neighborID] || !gotNodes[secondID] {
		t.Fatalf("nodes %+v", view.Nodes)
	}
	linked := map[string]bool{}
	for _, e := range view.Edges {
		if e.ID == between || (e.Source == neighborID && e.Target == secondID) || (e.Source == secondID && e.Target == neighborID) {
			t.Fatalf("neighbor edge included: %+v", view.Edges)
		}
		if e.Source == paperID {
			linked[e.Target] = true
		}
		if e.Target == paperID {
			linked[e.Source] = true
		}
	}
	if !linked[neighborID] || !linked[secondID] {
		t.Fatalf("paper edges %+v", view.Edges)
	}

	foreign, err := svc.Neighbors(ctx, owner, foreignID)
	if !errors.Is(err, apperr.ErrNotFound) {
		t.Fatalf("foreign err %v view %+v", err, foreign)
	}
	blob, marshalErr := json.Marshal(foreign)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	body := err.Error() + string(blob)
	if strings.Contains(body, foreignName) || strings.Contains(body, foreignTitle) {
		t.Fatalf("foreign name leaked: %s", body)
	}
}

func mustExec(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatal(err)
	}
}
