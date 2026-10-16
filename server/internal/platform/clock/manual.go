package clock

import (
	"sync"
	"time"
)

// Manual 供令牌过期测试注入时间。
type Manual struct {
	mu  sync.Mutex
	now time.Time
}

func NewManual(t time.Time) *Manual {
	return &Manual{now: t.UTC()}
}

func (m *Manual) Now() time.Time {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.now
}

func (m *Manual) Set(t time.Time) {
	m.mu.Lock()
	m.now = t.UTC()
	m.mu.Unlock()
}
