package graph

import (
	"context"
	"strings"
	"testing"
)

type testScope struct {
	owner string
	docs  []string
}

func (s testScope) OwnerID() string        { return s.owner }
func (s testScope) GraphDocIDs() []string { return s.docs }

func TestSupplementSQLPredicates(t *testing.T) {
	required := []string{
		"d.owner_id",
		"r.owner_id",
		"d.status = 'ready'",
		"r.suspended = false",
		"embedding_generation",
		"d.id = ANY",
		"hit_entities",
		"source_id",
		"target_id",
	}
	for _, needle := range required {
		if !strings.Contains(SupplementSQL, needle) {
			t.Fatalf("SupplementSQL missing predicate %q", needle)
		}
	}
}

func TestMemoryStoreSupplementCrossRelationOneHopEntityTraversal(t *testing.T) {
	store := NewMemoryStore()
	owner := "user-1"

	// r1: PaperA --uses_dataset--> ImageNet (种子切块 c-a)
	store.SeedEntityRelation("r1", owner, "paper:doc-a", "dataset:imagenet", false, []MemoryEvidence{
		{OwnerID: owner, DocumentID: "doc-a", ChunkID: "c-a", DocReady: true, Generation: 2},
	})
	// r2: PaperB --uses_dataset--> ImageNet (一跳共享实体 dataset:imagenet -> c-b 应命中)
	store.SeedEntityRelation("r2", owner, "paper:doc-b", "dataset:imagenet", false, []MemoryEvidence{
		{OwnerID: owner, DocumentID: "doc-b", ChunkID: "c-b", DocReady: true, Generation: 2},
	})
	// r3: PaperB --proposes_method--> MethodZ (距 c-a 命中实体为两跳 -> c-2hop 严禁出现)
	store.SeedEntityRelation("r3", owner, "paper:doc-b", "method:z", false, []MemoryEvidence{
		{OwnerID: owner, DocumentID: "doc-b", ChunkID: "c-2hop", DocReady: true, Generation: 2},
	})
	// r-susp: 挂起关系共享 dataset:imagenet -> c-susp 必须忽略
	store.SeedEntityRelation("r-susp", owner, "paper:doc-b", "dataset:imagenet", true, []MemoryEvidence{
		{OwnerID: owner, DocumentID: "doc-b", ChunkID: "c-susp", DocReady: true, Generation: 2},
	})
	// r-other: 其他用户共享同名字符串 -> c-other 必须隔离
	store.SeedEntityRelation("r-other", "user-2", "paper:doc-other", "dataset:imagenet", false, []MemoryEvidence{
		{OwnerID: "user-2", DocumentID: "doc-other", ChunkID: "c-other", DocReady: true, Generation: 2},
	})
	// r-unready / r-stale / r-out: 未完成、旧代际、不在 scope -> 必须过滤
	store.SeedEntityRelation("r-unready", owner, "paper:doc-unready", "dataset:imagenet", false, []MemoryEvidence{
		{OwnerID: owner, DocumentID: "doc-unready", ChunkID: "c-unready", DocReady: false, Generation: 2},
	})
	store.SeedEntityRelation("r-stale", owner, "paper:doc-b", "dataset:imagenet", false, []MemoryEvidence{
		{OwnerID: owner, DocumentID: "doc-b", ChunkID: "c-stale", DocReady: true, Generation: 1},
	})
	store.SeedEntityRelation("r-out", owner, "paper:doc-out", "dataset:imagenet", false, []MemoryEvidence{
		{OwnerID: owner, DocumentID: "doc-out", ChunkID: "c-out", DocReady: true, Generation: 2},
	})

	scope := testScope{owner: owner, docs: []string{"doc-a", "doc-b", "doc-unready"}}
	got, err := store.Supplement(context.Background(), scope, []string{"c-a"}, 2, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "c-b" {
		t.Fatalf("expected 1-hop entity traversal [c-b], got %v", got)
	}
}

func TestMemoryStoreSupplementRespectsScopeSuspendedReadyAndLimit(t *testing.T) {
	store := NewMemoryStore()
	// Rel 1: connects c1 (docA in KB A) -> c2 (docA) and c3 (docB, out of KB A scope!)
	store.SeedRelation("r1", "user-1", false, []MemoryEvidence{
		{OwnerID: "user-1", DocumentID: "doc-a", ChunkID: "c1", DocReady: true, Generation: 1},
		{OwnerID: "user-1", DocumentID: "doc-a", ChunkID: "c2", DocReady: true, Generation: 1},
		{OwnerID: "user-1", DocumentID: "doc-b", ChunkID: "c3", DocReady: true, Generation: 1},
	})
	// Rel 2: c1 -> c4 (docA), also in Rel 3 so c4 has higher evidence count (2 vs 1 for c2)
	store.SeedRelation("r2", "user-1", false, []MemoryEvidence{
		{OwnerID: "user-1", DocumentID: "doc-a", ChunkID: "c1", DocReady: true, Generation: 1},
		{OwnerID: "user-1", DocumentID: "doc-a", ChunkID: "c4", DocReady: true, Generation: 1},
	})
	store.SeedRelation("r3", "user-1", false, []MemoryEvidence{
		{OwnerID: "user-1", DocumentID: "doc-a", ChunkID: "c1", DocReady: true, Generation: 1},
		{OwnerID: "user-1", DocumentID: "doc-a", ChunkID: "c4", DocReady: true, Generation: 1},
	})
	// Rel 4: suspended relation -> c5 MUST be ignored
	store.SeedRelation("r4", "user-1", true, []MemoryEvidence{
		{OwnerID: "user-1", DocumentID: "doc-a", ChunkID: "c1", DocReady: true, Generation: 1},
		{OwnerID: "user-1", DocumentID: "doc-a", ChunkID: "c5", DocReady: true, Generation: 1},
	})
	// Rel 5: wrong generation / unready -> c6 MUST be ignored
	store.SeedRelation("r5", "user-1", false, []MemoryEvidence{
		{OwnerID: "user-1", DocumentID: "doc-a", ChunkID: "c1", DocReady: true, Generation: 1},
		{OwnerID: "user-1", DocumentID: "doc-a", ChunkID: "c6", DocReady: true, Generation: 2},
	})

	got, err := store.Supplement(context.Background(), testScope{owner: "user-1", docs: []string{"doc-a"}}, []string{"c1"}, 1, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "c4" || got[1] != "c2" {
		t.Fatalf("expected [c4 c2] strictly within doc-a, got %v", got)
	}
}
