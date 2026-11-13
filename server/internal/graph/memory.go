package graph

import (
	"context"
	"sort"
	"sync"
)

// MemoryEvidence 描述内存图谱中支撑某条关系的证据切块。
type MemoryEvidence struct {
	OwnerID    string
	DocumentID string
	ChunkID    string
	DocReady   bool
	Generation int64
}

type memoryRelation struct {
	ID           string
	OwnerID      string
	SourceEntity string
	TargetEntity string
	Suspended    bool
	Evidence     []MemoryEvidence
}

// MemoryStore 是用于 --in-memory 与 CI 测试的内存图谱补充实现，强制执行与 PG 一致的范围、代际与暂停规则。
type MemoryStore struct {
	mu        sync.RWMutex
	relations map[string]memoryRelation
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		relations: make(map[string]memoryRelation),
	}
}

// SeedRelation 写入一条图谱关系及其两侧证据（无显式实体端点，兼容仅测同关系共现的调用方）。
func (m *MemoryStore) SeedRelation(relationID, ownerID string, suspended bool, evidence []MemoryEvidence) {
	m.SeedEntityRelation(relationID, ownerID, "", "", suspended, evidence)
}

// SeedEntityRelation 写入一条带实体端点的一跳图谱关系及其证据，供跨关系一跳实体遍历测试使用。
func (m *MemoryStore) SeedEntityRelation(relationID, ownerID, sourceEntity, targetEntity string, suspended bool, evidence []MemoryEvidence) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]MemoryEvidence, len(evidence))
	copy(cp, evidence)
	m.relations[relationID] = memoryRelation{
		ID:           relationID,
		OwnerID:      ownerID,
		SourceEntity: sourceEntity,
		TargetEntity: targetEntity,
		Suspended:    suspended,
		Evidence:     cp,
	}
}

// Supplement 返回同关系共现或沿命中实体一跳可达、且严格落在 scope.GraphDocIDs() 内、非暂停、已 ready、同代际的补充切块。
func (m *MemoryStore) Supplement(ctx context.Context, scope SupplementScope, chunkIDs []string, generation int64, limit int) ([]string, error) {
	if scope == nil || len(chunkIDs) == 0 || limit <= 0 {
		return nil, nil
	}
	docIDs := scope.GraphDocIDs()
	if len(docIDs) == 0 {
		return nil, nil
	}
	allowedDocs := make(map[string]struct{}, len(docIDs))
	for _, did := range docIDs {
		allowedDocs[did] = struct{}{}
	}
	seedChunks := make(map[string]struct{}, len(chunkIDs))
	for _, cid := range chunkIDs {
		seedChunks[cid] = struct{}{}
	}
	owner := scope.OwnerID()

	m.mu.RLock()
	defer m.mu.RUnlock()

	isEligibleEvidence := func(ev MemoryEvidence) bool {
		if ev.OwnerID != owner || !ev.DocReady || ev.Generation != generation {
			return false
		}
		_, inScope := allowedDocs[ev.DocumentID]
		return inScope
	}

	// 第一步：收集种子切块命中的有效关系及两端实体。
	seedMatchedRel := make(map[string]struct{})
	hitEntities := make(map[string]struct{})
	for _, rel := range m.relations {
		if rel.OwnerID != owner || rel.Suspended {
			continue
		}
		hasValidSeed := false
		for _, ev := range rel.Evidence {
			if _, ok := seedChunks[ev.ChunkID]; ok && isEligibleEvidence(ev) {
				hasValidSeed = true
				break
			}
		}
		if !hasValidSeed {
			continue
		}
		seedMatchedRel[rel.ID] = struct{}{}
		if rel.SourceEntity != "" {
			hitEntities[rel.SourceEntity] = struct{}{}
		}
		if rel.TargetEntity != "" {
			hitEntities[rel.TargetEntity] = struct{}{}
		}
	}

	// 第二步：遍历同关系或连接命中实体的非暂停关系，收集符合范围与代际要求的候选切块。
	relCount := make(map[string]map[string]struct{})
	for _, rel := range m.relations {
		if rel.OwnerID != owner || rel.Suspended {
			continue
		}
		_, sameRel := seedMatchedRel[rel.ID]
		_, viaSource := hitEntities[rel.SourceEntity]
		_, viaTarget := hitEntities[rel.TargetEntity]
		if !sameRel && !viaSource && !viaTarget {
			continue
		}
		for _, ev := range rel.Evidence {
			if _, inSeed := seedChunks[ev.ChunkID]; inSeed {
				continue
			}
			if !isEligibleEvidence(ev) {
				continue
			}
			if relCount[ev.ChunkID] == nil {
				relCount[ev.ChunkID] = make(map[string]struct{})
			}
			relCount[ev.ChunkID][rel.ID] = struct{}{}
		}
	}

	type scored struct {
		chunkID string
		count   int
	}
	ranked := make([]scored, 0, len(relCount))
	for cid, rels := range relCount {
		ranked = append(ranked, scored{chunkID: cid, count: len(rels)})
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].count == ranked[j].count {
			return ranked[i].chunkID < ranked[j].chunkID
		}
		return ranked[i].count > ranked[j].count
	})
	if len(ranked) > limit {
		ranked = ranked[:limit]
	}
	out := make([]string, len(ranked))
	for i, item := range ranked {
		out[i] = item.chunkID
	}
	return out, nil
}
