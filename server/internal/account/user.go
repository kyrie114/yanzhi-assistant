package account

import "time"

const (
	roleUser  = "user"
	roleAdmin = "admin"
)

// User 是账户记录，字段对应 data-design.md 的 users。
type User struct {
	ID                  string
	Email               string
	Name                string
	PasswordHash        string
	Role                string
	EmailVerifiedAt     *time.Time
	TokensInvalidBefore *time.Time
	StorageBytes        int64
	CreatedAt           time.Time
}

// PublicUser 是注册和 GET /me 返回的公开信息，不含密码哈希和令牌。
type PublicUser struct {
	ID            string `json:"id"`
	Email         string `json:"email"`
	Name          string `json:"name"`
	Role          string `json:"role"`
	EmailVerified bool   `json:"email_verified"`
	StorageBytes  int64  `json:"storage_bytes"`
}

// Caller 是一张已经通过校验的访问令牌及其用户。
type Caller struct {
	User      User
	JTI       string
	ExpiresAt time.Time
}

func (u User) Public() PublicUser {
	role := "注册用户"
	if u.Role == roleAdmin {
		role = "系统管理员"
	}
	return PublicUser{
		ID:            u.ID,
		Email:         u.Email,
		Name:          u.Name,
		Role:          role,
		EmailVerified: u.EmailVerifiedAt != nil,
		StorageBytes:  u.StorageBytes,
	}
}

func copyUser(u User) User {
	if u.EmailVerifiedAt != nil {
		t := *u.EmailVerifiedAt
		u.EmailVerifiedAt = &t
	}
	if u.TokensInvalidBefore != nil {
		t := *u.TokensInvalidBefore
		u.TokensInvalidBefore = &t
	}
	return u
}
