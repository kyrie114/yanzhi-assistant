package qa

import (
	"context"
	"time"

	"yanzhi/server/internal/graph"
	"yanzhi/server/internal/knowledge"
	"yanzhi/server/internal/provider"
	"yanzhi/server/internal/retrieval"
)

// AskInput 表示单次问答请求参数。
type AskInput struct {
	Question  string               `json:"question"`
	Scope     knowledge.ScopeInput `json:"scope"`
	WebSearch bool                 `json:"web_search"`
}

// Session 表示问答会话列表及详情视图。
type Session struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
	Turns     []Turn `json:"turns,omitempty"`
}

// Turn 表示会话中已完整验证并落库的一轮问答。
type Turn struct {
	ID         string     `json:"id"`
	Question   string     `json:"question"`
	Answer     string     `json:"answer"`
	AnswerKind string     `json:"answer_kind"`
	Rewritten  bool       `json:"rewritten"`
	Reranked   bool       `json:"reranked"`
	CreatedAt  string     `json:"created_at"`
	Citations  []Citation `json:"citations"`
}

// Citation 表示向调用方返回的持久化出处条目。
type Citation struct {
	ChunkID         *string `json:"chunk_id"`
	DocumentID      *string `json:"document_id"`
	Label           string  `json:"label"`
	Origin          string  `json:"origin"`
	GraphSupplement bool    `json:"graph_supplement"`
}

// HistoryTurn 是多轮改写与生成提示词消费的精简历史问答对。
type HistoryTurn struct {
	Question string
	Answer   string
}

// PersistCitation 是写入 qa_citations 表的完整快照记录。
type PersistCitation struct {
	ChunkID         string
	DocumentID      string
	TitleSnapshot   string
	PageStart       int
	Origin          string
	GraphSupplement bool
	Label           string
}

// PersistTurn 是单次原子提交到 qa_turns + qa_citations 的完整领域记录。
type PersistTurn struct {
	ID            string
	SessionID     string
	Question      string
	Answer        string
	AnswerKind    string // "kb" | "smalltalk" | "refused"
	Rewritten     bool
	Reranked      bool
	WebSearch     bool
	Scope         knowledge.ScopeInput
	CandidateIDs  []string
	GraphExtraIDs []string
	Citations     []PersistCitation
	CreatedAt     time.Time
}

// TurnHandle 在流式执行启动后返回 turn_id 与当前事件订阅游标。
type TurnHandle struct {
	TurnID string
	Stream *StreamSubscription
}

// Store 抽象会话与问答轮次的持久化实现（PostgreSQL 与 MemoryStore 对等实现）。
type Store interface {
	CreateSession(ctx context.Context, owner, title string) (Session, error)
	ListSessions(ctx context.Context, owner string, page, size int) ([]Session, int, error)
	GetSession(ctx context.Context, owner, id string) (Session, error)
	EnsureSessionOwner(ctx context.Context, owner, id string) (string, error) // 返回当前会话标题；非所有者或不存在返回 ErrNotFound
	PatchSession(ctx context.Context, owner, id, title string) (Session, error)
	DeleteSession(ctx context.Context, owner, id string) error
	RecentHistory(ctx context.Context, owner, sessionID string, limit int) ([]HistoryTurn, error)
	SaveTurn(ctx context.Context, currentSessionTitle string, turn PersistTurn) error
	TurnExists(ctx context.Context, owner, sessionID, turnID string) (bool, error)
}

// Retriever 抽象检索与按范围水合子/父块的最小端口（由 *retrieval.Engine 满足）。
type Retriever interface {
	Search(ctx context.Context, reqID string, scope retrieval.Scope, query string) (retrieval.SearchResult, error)
	Hydrate(ctx context.Context, scope retrieval.Scope, chunkIDs []string) ([]retrieval.Hit, error)
}

// GraphPort 抽象受范围约束的一跳图谱补充端口（由 graph.Service 与 graph.MemoryStore 满足）。
type GraphPort interface {
	Supplement(ctx context.Context, scope graph.SupplementScope, chunkIDs []string, generation int64, limit int) ([]string, error)
}

// 以下接口为 Slice 2/3 未建表功能预留可选扩展槽位，默认可为 nil：
type PreferenceReader interface {
	LoadConfirmed(ctx context.Context, owner string) (string, error)
}

type FAQMatcher interface {
	Match(ctx context.Context, scope knowledge.ResolvedScope, query string) (answer string, matched bool, err error)
}

type WebSearcher interface {
	SearchWeb(ctx context.Context, query string, limit int) ([]WebSource, error)
}

type WebSource struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet"`
}

type FeedbackRecorder interface {
	PutFeedback(ctx context.Context, owner, turnID, vote, reason string) error
}

// 确保 provider.Gateway 被 qa 显式依赖，保持编译期边界清晰。
var _ provider.Gateway = (*provider.MemoryGateway)(nil)
