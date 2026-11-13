package knowledge

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"yanzhi/server/internal/apperr"
	"yanzhi/server/internal/platform/db"
)

// ScopeInput 描述一次问答请求的检索范围输入。
type ScopeInput struct {
	Type        string   `json:"type"`                   // "all" | "kb" | "tags" | "documents"
	KBID        string   `json:"kb_id,omitempty"`        // Type == "kb" 时必填
	TagIDs      []string `json:"tag_ids,omitempty"`      // Type == "tags" 时必填
	DocumentIDs []string `json:"document_ids,omitempty"` // Type == "documents" 时必填
}

// ResolvedScope 是仅由 ScopeResolver 构造的不可伪造能力令牌。
// 下游 retrieval 与 graph 只读取其方法而无法伪造文档集合，从类型上消除越权、未完成资料及跨库泄漏。
type ResolvedScope struct {
	ownerID           string
	input             ScopeInput
	docIDs            []string
	graphDocIDs       []string
	singleKB          bool
	answerInstruction string
	threshold         *float64
}

func (s ResolvedScope) OwnerID() string        { return s.ownerID }
func (s ResolvedScope) DocIDs() []string       { return slices.Clone(s.docIDs) }
func (s ResolvedScope) GraphDocIDs() []string  { return slices.Clone(s.graphDocIDs) }
func (s ResolvedScope) SingleKB() bool         { return s.singleKB }
func (s ResolvedScope) AnswerInstruction() string {
	return s.answerInstruction
}
func (s ResolvedScope) ThresholdOr(fallback float64) float64 {
	if s.threshold != nil {
		return *s.threshold
	}
	return fallback
}
func (s ResolvedScope) WireScope() ScopeInput { return s.input }

// EmptyScope 返回不含任何可检索文档的安全空范围，供纯寒暄绕过 NO_READY_DOCUMENTS 时使用。
func EmptyScope(owner string, in ScopeInput) ResolvedScope {
	return ResolvedScope{
		ownerID: owner,
		input:   normalizeScopeInput(in),
	}
}

// ChunkDetail 表示切片点击溯源接口返回的切片正文、起始物理页与所属父块文本。
type ChunkDetail struct {
	Text       string `json:"text"`
	PageStart  int    `json:"page_start"`
	ParentText string `json:"parent_text"`
}

// ScopeResolver 将用户范围输入收紧为本人、已就绪且经过归属校验的文档范围。
type ScopeResolver interface {
	ResolveScope(ctx context.Context, owner string, in ScopeInput) (ResolvedScope, error)
}

func normalizeScopeInput(in ScopeInput) ScopeInput {
	in.Type = strings.TrimSpace(in.Type)
	in.KBID = strings.TrimSpace(in.KBID)
	in.TagIDs = cleanIDList(in.TagIDs)
	in.DocumentIDs = cleanIDList(in.DocumentIDs)
	if in.Type == "" && in.KBID == "" && len(in.TagIDs) == 0 && len(in.DocumentIDs) == 0 {
		in.Type = "all"
	}
	return in
}

func cleanIDList(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, item := range in {
		trimmed := strings.TrimSpace(item)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	return out
}

func validateScopeInput(in ScopeInput) (ScopeInput, error) {
	in = normalizeScopeInput(in)
	switch in.Type {
	case "all":
		return in, nil
	case "kb":
		if in.KBID == "" {
			return ScopeInput{}, apperr.WithDetails("VALIDATION_ERROR", "请选择知识库", map[string]any{"field": "scope.kb_id"})
		}
		return in, nil
	case "tags":
		if len(in.TagIDs) == 0 {
			return ScopeInput{}, apperr.WithDetails("VALIDATION_ERROR", "请至少选择一个标签", map[string]any{"field": "scope.tag_ids"})
		}
		return in, nil
	case "documents":
		if len(in.DocumentIDs) == 0 {
			return ScopeInput{}, apperr.WithDetails("VALIDATION_ERROR", "请至少选择一篇资料", map[string]any{"field": "scope.document_ids"})
		}
		return in, nil
	default:
		return ScopeInput{}, apperr.WithDetails("VALIDATION_ERROR", "检索范围类型不正确", map[string]any{"field": "scope.type"})
	}
}

// ResolveScope 按参数(422) -> 目标归属(404) -> 就绪文档筛选(409 NO_READY_DOCUMENTS) 的顺序解析范围。
func (s Service) ResolveScope(ctx context.Context, owner string, rawIn ScopeInput) (ResolvedScope, error) {
	in, err := validateScopeInput(rawIn)
	if err != nil {
		return ResolvedScope{}, err
	}
	ownerUUID, ok := db.UUID(owner)
	if !ok {
		return ResolvedScope{}, apperr.ErrNotFound
	}

	resolved := ResolvedScope{
		ownerID: owner,
		input:   in,
	}

	var rows pgx.Rows
	switch in.Type {
	case "kb":
		kbUUID, ok := db.UUID(in.KBID)
		if !ok {
			return ResolvedScope{}, apperr.ErrNotFound
		}
		var instr *string
		var thresh *float64
		err := s.Pool.QueryRow(ctx, `
			SELECT answer_instruction, retrieval_threshold::float8
			FROM knowledge_bases
			WHERE id = $1 AND owner_id = $2`, kbUUID, ownerUUID).Scan(&instr, &thresh)
		if errors.Is(err, pgx.ErrNoRows) {
			return ResolvedScope{}, apperr.ErrNotFound
		}
		if err != nil {
			return ResolvedScope{}, err
		}
		resolved.singleKB = true
		if instr != nil {
			resolved.answerInstruction = *instr
		}
		resolved.threshold = thresh

		rows, err = s.Pool.Query(ctx, `
			SELECT d.id::text, k.graph_enabled
			FROM documents d
			JOIN knowledge_bases k ON k.id = d.kb_id
			WHERE d.owner_id = $1 AND d.kb_id = $2 AND d.status = 'ready'
			ORDER BY d.created_at, d.id`, ownerUUID, kbUUID)
		if err != nil {
			return ResolvedScope{}, err
		}
	case "tags":
		tagUUIDs, ok := db.UUIDs(in.TagIDs)
		if !ok {
			return ResolvedScope{}, apperr.ErrNotFound
		}
		var ownedCount int
		if err := s.Pool.QueryRow(ctx, `
			SELECT count(DISTINCT id) FROM tags WHERE owner_id = $1 AND id = ANY($2::uuid[])`,
			ownerUUID, tagUUIDs).Scan(&ownedCount); err != nil {
			return ResolvedScope{}, err
		}
		if ownedCount != len(in.TagIDs) {
			return ResolvedScope{}, apperr.ErrNotFound
		}
		rows, err = s.Pool.Query(ctx, `
			SELECT DISTINCT d.id::text, k.graph_enabled
			FROM documents d
			JOIN document_tags dt ON dt.document_id = d.id
			JOIN knowledge_bases k ON k.id = d.kb_id
			WHERE d.owner_id = $1 AND dt.tag_id = ANY($2::uuid[]) AND d.status = 'ready'`,
			ownerUUID, tagUUIDs)
		if err != nil {
			return ResolvedScope{}, err
		}
	case "documents":
		docUUIDs, ok := db.UUIDs(in.DocumentIDs)
		if !ok {
			return ResolvedScope{}, apperr.ErrNotFound
		}
		var ownedCount int
		if err := s.Pool.QueryRow(ctx, `
			SELECT count(DISTINCT id) FROM documents WHERE owner_id = $1 AND id = ANY($2::uuid[])`,
			ownerUUID, docUUIDs).Scan(&ownedCount); err != nil {
			return ResolvedScope{}, err
		}
		if ownedCount != len(in.DocumentIDs) {
			return ResolvedScope{}, apperr.ErrNotFound
		}
		rows, err = s.Pool.Query(ctx, `
			SELECT d.id::text, k.graph_enabled
			FROM documents d
			JOIN knowledge_bases k ON k.id = d.kb_id
			WHERE d.owner_id = $1 AND d.id = ANY($2::uuid[]) AND d.status = 'ready'`,
			ownerUUID, docUUIDs)
		if err != nil {
			return ResolvedScope{}, err
		}
	default: // "all"
		rows, err = s.Pool.Query(ctx, `
			SELECT d.id::text, k.graph_enabled
			FROM documents d
			JOIN knowledge_bases k ON k.id = d.kb_id
			WHERE d.owner_id = $1 AND d.status = 'ready'`,
			ownerUUID)
		if err != nil {
			return ResolvedScope{}, err
		}
	}
	defer rows.Close()

	for rows.Next() {
		var id string
		var graphEnabled bool
		if err := rows.Scan(&id, &graphEnabled); err != nil {
			return ResolvedScope{}, err
		}
		resolved.docIDs = append(resolved.docIDs, id)
		if graphEnabled {
			resolved.graphDocIDs = append(resolved.graphDocIDs, id)
		}
	}
	if err := rows.Err(); err != nil {
		return ResolvedScope{}, err
	}
	if len(resolved.docIDs) == 0 {
		return ResolvedScope{}, apperr.ErrNoReadyDocs
	}
	return resolved, nil
}

// ChunkDetail 返回指定切片及其所属父块文本，严格校验文档归属所有者。
func (s Service) ChunkDetail(ctx context.Context, owner, docID, chunkID string) (ChunkDetail, error) {
	ownerUUID, okOwner := db.UUID(owner)
	docUUID, okDoc := db.UUID(docID)
	chunkUUID, okChunk := db.UUID(chunkID)
	if !okOwner || !okDoc || !okChunk {
		return ChunkDetail{}, apperr.ErrNotFound
	}
	var out ChunkDetail
	var parentText pgtype.Text
	err := s.Pool.QueryRow(ctx, `
		SELECT c.text, c.page_start, p.text
		FROM chunks c
		LEFT JOIN parent_chunks p ON p.id = c.parent_id
		JOIN documents d ON d.id = c.document_id
		WHERE c.id = $1 AND d.id = $2 AND d.owner_id = $3`,
		chunkUUID, docUUID, ownerUUID).Scan(&out.Text, &out.PageStart, &parentText)
	if errors.Is(err, pgx.ErrNoRows) {
		return ChunkDetail{}, apperr.ErrNotFound
	}
	if err != nil {
		return ChunkDetail{}, err
	}
	if parentText.Valid {
		out.ParentText = parentText.String
	}
	return out, nil
}
