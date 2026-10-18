// Package suggest 提供快速开始和回答结束后的推荐问题。
// 点击一条问题只把它填进提问框，不自动发送。
// 资料完成后可以生成推荐；生成失败时这份列表为空，其他资料不受影响。
// 知识库的来源变化后，先返回已有推荐，并标记为正在更新。
package suggest

import (
	"errors"
	"strings"
	"sync"
)

const MaxQuestions = 5

// ActionFill 表示客户端把问题写入提问框，不提交。
const ActionFill = "fill"

const (
	StatusReady      = "ready"
	StatusGenerating = "generating"
)

var ErrNotFound = errors.New("not found")

// QuestionGenerator 根据资料文本生成推荐问题。
type QuestionGenerator interface {
	SuggestQuestions(texts []string) ([]string, error)
}

// Result 是一次推荐。Questions 为空切片表示没有可展示的问题。
// 知识库响应带 Status。资料响应不带。
type Result struct {
	Questions []string `json:"questions"`
	Action    string   `json:"action"`
	Status    string   `json:"status,omitempty"`
}

type item struct {
	OwnerID           string
	Ready             bool
	Questions         []string
	SourceFingerprint string
	CacheFingerprint  string
	CacheValid        bool
	Generating        bool
	Docs              []SourceDoc
}

// Memory 按资料或知识库保存已经生成的推荐问题。
type Memory struct {
	mu        sync.Mutex
	docs      map[string]item
	kbs       map[string]item
	model     QuestionGenerator
	onKBStale func(id, owner, fingerprint string)
}

func NewMemory() *Memory {
	return &Memory{docs: map[string]item{}, kbs: map[string]item{}}
}

func (m *Memory) PutDocument(id, owner string, ready bool, questions []string) {
	m.put(m.docs, id, owner, ready, questions)
}

func (m *Memory) PutKB(id, owner string, ready bool, questions []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.kbs[id] = item{
		OwnerID:    owner,
		Ready:      ready,
		Questions:  append([]string(nil), questions...),
		CacheValid: true,
	}
}

// OnKBStale 在打开知识库发现推荐过期时调用一次。
// 回调里可以生成并 SaveKB。这次 List 仍返回调用前的列表。
func (m *Memory) OnKBStale(fn func(id, owner, fingerprint string)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.onKBStale = fn
}

// SetKBSource 记录知识库当前已完成资料的指纹。指纹变化后，下次打开会触发更新。
func (m *Memory) SetKBSource(id, owner, fingerprint string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	row, ok := m.kbs[id]
	if ok && row.OwnerID != owner {
		return
	}
	if !ok {
		row = item{OwnerID: owner}
	}
	row.Ready = true
	if row.SourceFingerprint != fingerprint {
		row.SourceFingerprint = fingerprint
		row.Generating = false
	}
	m.kbs[id] = row
}

// SaveKB 写入一次知识库推荐生成的结果。fingerprint 必须仍是当前来源，过期结果会被丢掉。
// failed 为 true 时不展示推荐问题。
func (m *Memory) SaveKB(id, owner, fingerprint string, questions []string, failed bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	row, ok := m.kbs[id]
	if !ok || row.OwnerID != owner || row.SourceFingerprint != fingerprint {
		return
	}
	row.Ready = true
	row.Generating = false
	row.CacheValid = true
	row.CacheFingerprint = fingerprint
	if failed {
		row.Questions = []string{}
	} else {
		row.Questions = normalize(questions)
	}
	m.kbs[id] = row
}

// GenerateDocument 在资料完成后生成推荐问题。
// 资料未完成时不调用生成器。生成失败时只清空这份推荐。
func (m *Memory) GenerateDocument(id, owner string, ready bool, texts []string, model QuestionGenerator) {
	if !ready {
		m.PutDocument(id, owner, false, nil)
		return
	}
	var questions []string
	if model != nil {
		got, err := model.SuggestQuestions(texts)
		if err == nil {
			questions = normalize(got)
		}
	}
	m.PutDocument(id, owner, true, questions)
}

func (m *Memory) put(dst map[string]item, id, owner string, ready bool, questions []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	dst[id] = item{OwnerID: owner, Ready: ready, Questions: append([]string(nil), questions...)}
}

// List 返回快速开始问题。asked 非空时表示回答刚结束，去掉刚问过的那一句。
// 知识库推荐过期时先返回已有问题，并把 Status 标成 generating，再按当时的资料文本更新。
func (m *Memory) List(caller, scopeType, scopeID, asked string) (Result, error) {
	m.mu.Lock()
	result, pending, err := m.listLocked(caller, scopeType, scopeID, asked)
	var hook func(string, string, string)
	if pending.fire {
		hook = m.onKBStale
	}
	model := m.model
	m.mu.Unlock()
	if pending.fire && model != nil {
		m.refreshKB(pending, model)
	}
	if hook != nil {
		hook(pending.id, pending.owner, pending.fingerprint)
	}
	return result, err
}

type refresh struct {
	id, owner, fingerprint string
	docs                   []SourceDoc
	fire                   bool
}

func (m *Memory) listLocked(caller, scopeType, scopeID, asked string) (Result, refresh, error) {
	var table map[string]item
	switch scopeType {
	case "document":
		table = m.docs
	case "kb":
		table = m.kbs
	default:
		return Result{}, refresh{}, ErrNotFound
	}
	row, ok := table[scopeID]
	if !ok || row.OwnerID != caller {
		return Result{}, refresh{}, ErrNotFound
	}
	if !row.Ready {
		return empty(), refresh{}, nil
	}
	result := Result{Questions: pick(row.Questions, asked), Action: ActionFill}
	if scopeType != "kb" {
		return result, refresh{}, nil
	}
	result.Status = StatusReady
	if row.CacheValid && row.CacheFingerprint == row.SourceFingerprint {
		return result, refresh{}, nil
	}
	result.Status = StatusGenerating
	if row.Generating {
		return result, refresh{}, nil
	}
	row.Generating = true
	table[scopeID] = row
	return result, refresh{
		id: scopeID, owner: row.OwnerID, fingerprint: row.SourceFingerprint,
		docs: cloneDocs(row.Docs), fire: true,
	}, nil
}

func empty() Result {
	return Result{Questions: []string{}, Action: ActionFill}
}

func pick(questions []string, asked string) []string {
	asked = strings.TrimSpace(asked)
	seen := map[string]bool{}
	out := make([]string, 0, MaxQuestions)
	for _, q := range questions {
		q = strings.TrimSpace(q)
		if q == "" || (asked != "" && q == asked) || seen[q] {
			continue
		}
		seen[q] = true
		out = append(out, q)
		if len(out) == MaxQuestions {
			break
		}
	}
	return out
}

func normalize(questions []string) []string {
	return pick(questions, "")
}
