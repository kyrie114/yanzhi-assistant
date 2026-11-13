package qa

import (
	"context"
	"encoding/json"
	"maps"
	"sync"
	"time"
)

// StreamEvent 表示带单调递增序号的 SSE 事件帧，可直接序列化到 SSE `id:`/`event:`/`data:`。
type StreamEvent struct {
	Seq  int64
	Name string
	Data json.RawMessage
}

// TurnStream 使用“写切片拷贝 + notify 条件广播”隔离单生产者和多消费者，
// 任何网络写与 DB I/O 均在锁外执行，且彻底消除有界 channel 丢事件和重放窗口竞态。
type TurnStream struct {
	turnID    string
	sessionID string
	ownerID   string
	cancel    context.CancelFunc

	mu            sync.Mutex
	events        []StreamEvent
	hasToken      bool
	stopRequested bool
	closed        bool
	terminalAt    time.Time
	notify        chan struct{}
}

func newTurnStream(turnID, sessionID, ownerID string, cancel context.CancelFunc) *TurnStream {
	return &TurnStream{
		turnID:    turnID,
		sessionID: sessionID,
		ownerID:   ownerID,
		cancel:    cancel,
		notify:    make(chan struct{}),
	}
}

func (ts *TurnStream) Publish(name string, payload any) {
	raw, err := json.Marshal(payload)
	if err != nil {
		raw = []byte("{}")
	}
	ts.mu.Lock()
	if ts.closed {
		ts.mu.Unlock()
		return
	}
	seq := int64(len(ts.events)) + 1
	ts.events = append(ts.events, StreamEvent{
		Seq:  seq,
		Name: name,
		Data: raw,
	})
	if name == "token" {
		ts.hasToken = true
	}
	close(ts.notify)
	ts.notify = make(chan struct{})
	ts.mu.Unlock()
}

func (ts *TurnStream) RequestStop() {
	ts.mu.Lock()
	ts.stopRequested = true
	cancel := ts.cancel
	ts.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (ts *TurnStream) StopRequested() bool {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return ts.stopRequested
}

func (ts *TurnStream) HasToken() bool {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return ts.hasToken
}

func (ts *TurnStream) CloseTerminal(now time.Time) {
	ts.mu.Lock()
	if !ts.closed {
		ts.closed = true
		ts.terminalAt = now
		close(ts.notify)
	}
	ts.mu.Unlock()
}

func (ts *TurnStream) Subscribe(afterSeq int64) *StreamSubscription {
	if afterSeq < 0 {
		afterSeq = 0
	}
	return &StreamSubscription{
		stream: ts,
		cursor: afterSeq,
	}
}

// StreamSubscription 允许多个 HTTP 消费者按自身游标读取事件。
type StreamSubscription struct {
	stream *TurnStream
	cursor int64
}

// Next 返回游标之后的下一帧事件；流正常终结且无待读事件时返回 ok=false；HTTP 上下文取消也返回 ok=false。
func (sub *StreamSubscription) Next(ctx context.Context) (StreamEvent, bool) {
	for {
		sub.stream.mu.Lock()
		total := int64(len(sub.stream.events))
		if sub.cursor < total {
			evt := sub.stream.events[sub.cursor]
			sub.cursor++
			sub.stream.mu.Unlock()
			return evt, true
		}
		if sub.stream.closed {
			sub.stream.mu.Unlock()
			return StreamEvent{}, false
		}
		ch := sub.stream.notify
		sub.stream.mu.Unlock()

		select {
		case <-ch:
		case <-ctx.Done():
			return StreamEvent{}, false
		}
	}
}

func (sub *StreamSubscription) Close() {}

// StreamBroker 管理进程内活跃与已结束（15 分钟重放窗口）轮次的索引与停止路由。
type StreamBroker struct {
	ttl             time.Duration
	mu              sync.Mutex
	byTurn          map[string]*TurnStream
	activeBySession map[string]map[string]struct{}
}

func NewStreamBroker(ttl time.Duration) *StreamBroker {
	if ttl <= 0 {
		ttl = 15 * time.Minute
	}
	return &StreamBroker{
		ttl:             ttl,
		byTurn:          make(map[string]*TurnStream),
		activeBySession: make(map[string]map[string]struct{}),
	}
}

func (b *StreamBroker) Register(appCtx context.Context, owner, sessionID, turnID string, timeout time.Duration) (*TurnStream, context.Context) {
	baseCtx := appCtx
	if baseCtx == nil {
		baseCtx = context.Background()
	}
	var genCtx context.Context
	var cancel context.CancelFunc
	if timeout > 0 {
		genCtx, cancel = context.WithTimeout(baseCtx, timeout)
	} else {
		genCtx, cancel = context.WithCancel(baseCtx)
	}
	ts := newTurnStream(turnID, sessionID, owner, cancel)

	b.mu.Lock()
	b.byTurn[turnID] = ts
	if b.activeBySession[sessionID] == nil {
		b.activeBySession[sessionID] = make(map[string]struct{})
	}
	b.activeBySession[sessionID][turnID] = struct{}{}
	b.mu.Unlock()

	return ts, genCtx
}

func (b *StreamBroker) MarkFinished(ts *TurnStream, now time.Time) {
	ts.CloseTerminal(now)
	b.mu.Lock()
	if set, ok := b.activeBySession[ts.sessionID]; ok {
		delete(set, ts.turnID)
		if len(set) == 0 {
			delete(b.activeBySession, ts.sessionID)
		}
	}
	b.mu.Unlock()
}

func (b *StreamBroker) Lookup(owner, sessionID, turnID string) (*TurnStream, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	ts, ok := b.byTurn[turnID]
	if !ok || ts.ownerID != owner || ts.sessionID != sessionID {
		return nil, false
	}
	return ts, true
}

func (b *StreamBroker) StopSession(owner, sessionID string) {
	b.mu.Lock()
	ids := maps.Clone(b.activeBySession[sessionID])
	targets := make([]*TurnStream, 0, len(ids))
	for turnID := range ids {
		if ts, ok := b.byTurn[turnID]; ok && ts.ownerID == owner {
			targets = append(targets, ts)
		}
	}
	b.mu.Unlock()

	for _, ts := range targets {
		ts.RequestStop()
	}
}

// SweepExpired 清理超过 TTL 且已结束的流缓存，供后台定时循环与单测直接触发。
func (b *StreamBroker) SweepExpired(now time.Time) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	removed := 0
	for turnID, ts := range b.byTurn {
		ts.mu.Lock()
		expired := ts.closed && !ts.terminalAt.IsZero() && now.Sub(ts.terminalAt) >= b.ttl
		ts.mu.Unlock()
		if expired {
			delete(b.byTurn, turnID)
			removed++
		}
	}
	return removed
}

func (b *StreamBroker) startJanitor(appCtx context.Context, nowFn func() time.Time) {
	if appCtx == nil {
		return
	}
	interval := b.ttl / 5
	if interval < time.Second {
		interval = time.Second
	}
	if interval > time.Minute {
		interval = time.Minute
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-appCtx.Done():
				return
			case <-ticker.C:
				b.SweepExpired(nowFn())
			}
		}
	}()
}
