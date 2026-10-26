package account

import (
	"crypto/rand"
	"fmt"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"

	"yanzhi/server/internal/platform/clock"
)

const tokenTTL = 2 * time.Hour

type tokenClaims struct {
	Role           string `json:"role"`
	SessionVersion uint64 `json:"sv"`
	jwt.RegisteredClaims
}

// LoginResult 是登录成功后交给接口层的令牌。
type LoginResult struct {
	AccessToken   string
	ExpiresIn     int
	Role          string
	EmailVerified bool
}

// Service 实现注册、登录、单令牌退出和修改密码。
type Service struct {
	store      *MemoryStore
	clock      clock.Clock
	secret     []byte
	bcryptCost int
	dummy      []byte
}

func NewService(store *MemoryStore, clk clock.Clock, secret []byte, bcryptCost int) *Service {
	if clk == nil {
		clk = clock.System{}
	}
	if bcryptCost < bcrypt.MinCost {
		bcryptCost = bcrypt.DefaultCost
	}
	if len(secret) == 0 {
		panic("jwt secret is empty")
	}
	dummy, err := bcrypt.GenerateFromPassword([]byte("dummy-password-not-a-user"), bcryptCost)
	if err != nil {
		panic(err)
	}
	return &Service{
		store:      store,
		clock:      clk,
		secret:     append([]byte(nil), secret...),
		bcryptCost: bcryptCost,
		dummy:      dummy,
	}
}

// Register 创建注册用户。调用方不能通过参数指定角色。
func (s *Service) Register(email, name, password string) (User, error) {
	email, err := normalizeEmail(email)
	if err != nil {
		return User{}, err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return User{}, &ValidationError{Details: map[string]string{"name": "姓名不能为空"}}
	}
	if !ValidPassword(password) {
		return User{}, ErrWeakPassword
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), s.bcryptCost)
	if err != nil {
		return User{}, err
	}
	user := User{
		ID:           newUUID(),
		Email:        email,
		Name:         name,
		PasswordHash: string(hash),
		Role:         roleUser,
		StorageBytes: 0,
		CreatedAt:    s.clock.Now().UTC(),
	}
	if err := s.store.Create(user); err != nil {
		return User{}, err
	}
	return user, nil
}

// Login 校验邮箱和密码。邮箱不存在时仍做一次 bcrypt 比较，避免用响应时差区分两种失败。
func (s *Service) Login(email, password string) (LoginResult, error) {
	normalized, nerr := normalizeEmail(email)
	var user User
	var ok bool
	if nerr == nil {
		user, ok = s.store.FindByEmail(normalized)
	}
	hash := s.dummy
	if ok {
		hash = []byte(user.PasswordHash)
	}
	match := bcrypt.CompareHashAndPassword(hash, []byte(password)) == nil
	if !ok || !match {
		return LoginResult{}, ErrInvalidCredentials
	}
	raw, err := s.sign(user)
	if err != nil {
		return LoginResult{}, err
	}
	pub := user.Public()
	return LoginResult{
		AccessToken:   raw,
		ExpiresIn:     int(tokenTTL.Seconds()),
		Role:          pub.Role,
		EmailVerified: pub.EmailVerified,
	}, nil
}

// Logout 只把当前 jti 记入失效表。
func (s *Service) Logout(raw string) error {
	caller, err := s.Authenticate(raw)
	if err != nil {
		return err
	}
	return s.store.Revoke(caller.JTI, caller.User.ID, caller.ExpiresAt)
}

// ChangePassword 校验旧密码后原子更新哈希、会话版本和 tokens_invalid_before。
// 会话版本使同一秒内签发的旧令牌也立即失效；拒绝请求不修改账户。
func (s *Service) ChangePassword(raw, oldPassword, newPassword string) error {
	caller, err := s.Authenticate(raw)
	if err != nil {
		return err
	}
	if bcrypt.CompareHashAndPassword([]byte(caller.User.PasswordHash), []byte(oldPassword)) != nil {
		return ErrInvalidCredentials
	}
	if !ValidPassword(newPassword) {
		return ErrWeakPassword
	}
	next, err := bcrypt.GenerateFromPassword([]byte(newPassword), s.bcryptCost)
	if err != nil {
		return err
	}
	invalidBefore := s.clock.Now().UTC().Truncate(time.Second)
	return s.store.UpdatePassword(caller.User.ID, caller.User.PasswordHash, string(next), invalidBefore)
}

// Authenticate 校验签名、exp、jti，再核对会话版本和 tokens_invalid_before。
func (s *Service) Authenticate(raw string) (Caller, error) {
	claims, err := s.parse(raw)
	if err != nil {
		return Caller{}, ErrUnauthenticated
	}
	if s.store.IsRevoked(claims.ID) {
		return Caller{}, ErrUnauthenticated
	}
	user, ok := s.store.FindByID(claims.Subject)
	if !ok || claims.IssuedAt == nil || claims.ExpiresAt == nil {
		return Caller{}, ErrUnauthenticated
	}
	if claims.SessionVersion != user.SessionVersion {
		return Caller{}, ErrUnauthenticated
	}
	if user.TokensInvalidBefore != nil && claims.IssuedAt.Time.Before(*user.TokensInvalidBefore) {
		return Caller{}, ErrUnauthenticated
	}
	return Caller{User: user, JTI: claims.ID, ExpiresAt: claims.ExpiresAt.Time}, nil
}

func (s *Service) sign(user User) (string, error) {
	now := s.clock.Now().UTC().Truncate(time.Second)
	claims := tokenClaims{
		Role:           user.Role,
		SessionVersion: user.SessionVersion,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   user.ID,
			ID:        newUUID(),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(tokenTTL)),
		},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(s.secret)
}

func (s *Service) parse(raw string) (*tokenClaims, error) {
	parsed, err := jwt.ParseWithClaims(raw, &tokenClaims{}, func(t *jwt.Token) (any, error) {
		if t.Method != jwt.SigningMethodHS256 {
			return nil, ErrUnauthenticated
		}
		return s.secret, nil
	}, jwt.WithTimeFunc(func() time.Time { return s.clock.Now() }))
	if err != nil || !parsed.Valid {
		return nil, ErrUnauthenticated
	}
	claims, ok := parsed.Claims.(*tokenClaims)
	if !ok || claims.Subject == "" || claims.ID == "" {
		return nil, ErrUnauthenticated
	}
	return claims, nil
}

func normalizeEmail(raw string) (string, error) {
	email := strings.ToLower(strings.TrimSpace(raw))
	local, domain, ok := strings.Cut(email, "@")
	if !ok || local == "" || domain == "" || strings.ContainsAny(email, " \t") || strings.Count(email, "@") != 1 {
		return "", &ValidationError{Details: map[string]string{"email": "邮箱格式不正确"}}
	}
	return email, nil
}

func newUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}
