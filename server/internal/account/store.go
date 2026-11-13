package account

import (
	"sync"
	"time"

	"yanzhi/server/internal/apperr"
)

// revokedToken 对应 revoked_tokens。退出只插入当前 jti。
type revokedToken struct {
	UserID    string
	ExpiresAt time.Time
}

// MemoryStore 按 users 与 revoked_tokens 的字段保存账户。
// 本切片不连接 PostgreSQL，测试和接口使用这份内存数据。
type MemoryStore struct {
	mu      sync.Mutex
	byID    map[string]User
	byEmail map[string]string
	revoked map[string]revokedToken
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		byID:    map[string]User{},
		byEmail: map[string]string{},
		revoked: map[string]revokedToken{},
	}
}

func (m *MemoryStore) Create(user User) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.byEmail[user.Email]; exists {
		return ErrEmailTaken
	}
	m.byID[user.ID] = copyUser(user)
	m.byEmail[user.Email] = user.ID
	return nil
}

func (m *MemoryStore) FindByEmail(email string) (User, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id, ok := m.byEmail[email]
	if !ok {
		return User{}, false
	}
	return copyUser(m.byID[id]), true
}

func (m *MemoryStore) FindByID(id string) (User, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	user, ok := m.byID[id]
	if !ok {
		return User{}, false
	}
	return copyUser(user), true
}

// UpdatePassword uses the previously verified hash as a compare-and-swap guard.
// A delayed request cannot overwrite a password already changed by another one.
func (m *MemoryStore) UpdatePassword(id, expectedHash, hash string, invalidBefore time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	user, ok := m.byID[id]
	if !ok || user.PasswordHash != expectedHash {
		return ErrUnauthenticated
	}
	user.PasswordHash = hash
	user.SessionVersion++
	t := invalidBefore
	user.TokensInvalidBefore = &t
	m.byID[id] = user
	return nil
}

func (m *MemoryStore) Revoke(jti, userID string, expiresAt time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.revoked[jti] = revokedToken{UserID: userID, ExpiresAt: expiresAt}
	return nil
}

func (m *MemoryStore) IsRevoked(jti string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.revoked[jti]
	return ok
}

func (m *MemoryStore) GetStorageBytes(userID string) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	user, ok := m.byID[userID]
	if !ok {
		return 0, ErrUnauthenticated
	}
	return user.StorageBytes, nil
}

func (m *MemoryStore) ReserveStorage(userID string, delta, quotaBytes int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	user, ok := m.byID[userID]
	if !ok {
		return ErrUnauthenticated
	}
	if user.StorageBytes+delta > quotaBytes {
		return apperr.Quota(user.StorageBytes)
	}
	user.StorageBytes += delta
	m.byID[userID] = user
	return nil
}

func (m *MemoryStore) ReleaseStorage(userID string, delta int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	user, ok := m.byID[userID]
	if !ok {
		return ErrUnauthenticated
	}
	user.StorageBytes -= delta
	if user.StorageBytes < 0 {
		user.StorageBytes = 0
	}
	m.byID[userID] = user
	return nil
}

func (m *MemoryStore) SetStorageBytes(userID string, n int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	user, ok := m.byID[userID]
	if !ok {
		return ErrUnauthenticated
	}
	user.StorageBytes = n
	m.byID[userID] = user
	return nil
}

func (m *MemoryStore) SetEmailVerified(userID string, at *time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	user, ok := m.byID[userID]
	if !ok {
		return ErrUnauthenticated
	}
	if at != nil {
		t := *at
		user.EmailVerifiedAt = &t
	} else {
		user.EmailVerifiedAt = nil
	}
	m.byID[userID] = user
	return nil
}
