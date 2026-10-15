package identity

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
	"yanzhi/server/internal/apperr"
	"yanzhi/server/internal/platform/clock"
	"yanzhi/server/internal/platform/db"
)

const tokenTTL = 2 * time.Hour

type User struct {
	ID            string
	Email         string
	Name          string
	Role          string
	Verified      bool
	StorageBytes  int64
	InvalidBefore *time.Time
}

type PublicUser struct {
	ID            string `json:"id"`
	Email         string `json:"email"`
	Name          string `json:"name"`
	Role          string `json:"role"`
	EmailVerified bool   `json:"email_verified"`
	StorageBytes  int64  `json:"storage_bytes,omitempty"`
}

type Claims struct {
	Role string `json:"role"`
	jwt.RegisteredClaims
}

type Service struct {
	Pool   *pgxpool.Pool
	Secret []byte
	Cost   int
	Clock  clock.Clock
	dummy  []byte
}

func NewService(pool *pgxpool.Pool, secret []byte, cost int, clk clock.Clock) (*Service, error) {
	if clk == nil {
		clk = clock.System{}
	}
	if cost < bcrypt.MinCost {
		cost = bcrypt.DefaultCost
	}
	dummy, err := bcrypt.GenerateFromPassword([]byte("yanzhi-not-a-user"), cost)
	if err != nil {
		return nil, err
	}
	return &Service{Pool: pool, Secret: secret, Cost: cost, Clock: clk, dummy: dummy}, nil
}

func (s *Service) Register(ctx context.Context, email, name, password string) (PublicUser, error) {
	email = normalizeEmail(email)
	name = strings.TrimSpace(name)
	if !emailOK(email) {
		return PublicUser{}, apperr.WithDetails("VALIDATION_ERROR", "邮箱格式不正确", map[string]any{"field": "email"})
	}
	if n := len([]rune(name)); n < 1 || n > 50 {
		return PublicUser{}, apperr.WithDetails("VALIDATION_ERROR", "姓名需要 1 到 50 个字符", map[string]any{"field": "name"})
	}
	if !PasswordOK(password) {
		return PublicUser{}, apperr.ErrPasswordWeak
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), s.Cost)
	if err != nil {
		return PublicUser{}, apperr.ErrPasswordWeak
	}
	id := db.NewID()
	uid, _ := db.UUID(id)
	_, err = s.Pool.Exec(ctx, `
		INSERT INTO users (id, email, name, password_hash, role, storage_bytes, created_at)
		VALUES ($1, $2, $3, $4, 'user', 0, now())`, uid, email, name, string(hash))
	if err != nil {
		var pe *pgconn.PgError
		if errors.As(err, &pe) && pe.Code == "23505" {
			return PublicUser{}, apperr.ErrEmailTaken
		}
		return PublicUser{}, err
	}
	return PublicUser{ID: id, Email: email, Name: name, Role: RoleLabel("user"), EmailVerified: false}, nil
}

type LoginResult struct {
	AccessToken   string `json:"access_token"`
	TokenType     string `json:"token_type"`
	ExpiresIn     int    `json:"expires_in"`
	Role          string `json:"role"`
	EmailVerified bool   `json:"email_verified"`
}

func (s *Service) Login(ctx context.Context, email, password string) (LoginResult, error) {
	email = normalizeEmail(email)
	var (
		id, name, role, hash string
		verified             *time.Time
		invalid              *time.Time
	)
	err := s.Pool.QueryRow(ctx, `
		SELECT id::text, name, role, password_hash, email_verified_at, tokens_invalid_before
		FROM users WHERE email = $1`, email).Scan(&id, &name, &role, &hash, &verified, &invalid)
	compare := s.dummy
	if err == nil {
		compare = []byte(hash)
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return LoginResult{}, err
	}
	if bcrypt.CompareHashAndPassword(compare, []byte(password)) != nil || err != nil {
		return LoginResult{}, apperr.ErrCredentials
	}
	token, _, err := s.sign(id, role)
	if err != nil {
		return LoginResult{}, err
	}
	return LoginResult{
		AccessToken: token, TokenType: "Bearer", ExpiresIn: int(tokenTTL.Seconds()),
		Role: RoleLabel(role), EmailVerified: verified != nil,
	}, nil
}

func (s *Service) sign(userID, role string) (string, Claims, error) {
	now := s.Clock.Now()
	claims := Claims{
		Role: role,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   userID,
			ID:        db.NewID(),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(tokenTTL)),
		},
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := tok.SignedString(s.Secret)
	return signed, claims, err
}

type Principal struct {
	UserID   string
	Role     string
	Verified bool
	JTI      string
	IssuedAt time.Time
	Expires  time.Time
}

func (s *Service) Authenticate(ctx context.Context, raw string) (Principal, error) {
	claims := &Claims{}
	_, err := jwt.ParseWithClaims(raw, claims, func(t *jwt.Token) (any, error) {
		if t.Method != jwt.SigningMethodHS256 {
			return nil, apperr.ErrUnauthenticated
		}
		return s.Secret, nil
	}, jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}))
	if err != nil || claims.Subject == "" || claims.ID == "" || claims.IssuedAt == nil || claims.ExpiresAt == nil {
		return Principal{}, apperr.ErrUnauthenticated
	}
	var (
		role     string
		verified *time.Time
		invalid  *time.Time
		revoked  bool
	)
	err = s.Pool.QueryRow(ctx, `
		SELECT u.role, u.email_verified_at, u.tokens_invalid_before,
		       EXISTS(SELECT 1 FROM revoked_tokens r WHERE r.jti = $2::uuid)
		FROM users u WHERE u.id = $1::uuid`, claims.Subject, claims.ID).Scan(&role, &verified, &invalid, &revoked)
	if errors.Is(err, pgx.ErrNoRows) {
		return Principal{}, apperr.ErrUnauthenticated
	}
	if err != nil {
		return Principal{}, err
	}
	iat := claims.IssuedAt.Time
	exp := claims.ExpiresAt.Time
	if !TokenActive(iat, exp, s.Clock.Now(), invalid, revoked) {
		return Principal{}, apperr.ErrUnauthenticated
	}
	return Principal{
		UserID: claims.Subject, Role: role, Verified: verified != nil,
		JTI: claims.ID, IssuedAt: iat, Expires: exp,
	}, nil
}

func (s *Service) Logout(ctx context.Context, p Principal) error {
	jti, ok := db.UUID(p.JTI)
	if !ok {
		return apperr.ErrUnauthenticated
	}
	uid := db.MustUUID(p.UserID)
	_, err := s.Pool.Exec(ctx, `
		INSERT INTO revoked_tokens (jti, user_id, expires_at) VALUES ($1, $2, $3)
		ON CONFLICT (jti) DO NOTHING`, jti, uid, p.Expires)
	return err
}

func (s *Service) ChangePassword(ctx context.Context, userID, oldPassword, newPassword string) error {
	if !PasswordOK(newPassword) {
		return apperr.ErrPasswordWeak
	}
	var hash string
	err := s.Pool.QueryRow(ctx, `SELECT password_hash FROM users WHERE id = $1::uuid`, userID).Scan(&hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return apperr.ErrUnauthenticated
	}
	if err != nil {
		return err
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(oldPassword)) != nil {
		return apperr.ErrCredentials
	}
	next, err := bcrypt.GenerateFromPassword([]byte(newPassword), s.Cost)
	if err != nil {
		return apperr.ErrPasswordWeak
	}
	tag, err := s.Pool.Exec(ctx, `
		UPDATE users SET password_hash = $2, tokens_invalid_before = $3 WHERE id = $1::uuid`,
		userID, string(next), s.Clock.Now())
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return apperr.ErrUnauthenticated
	}
	return nil
}

func (s *Service) Me(ctx context.Context, userID string) (PublicUser, error) {
	var u PublicUser
	var role string
	var verified *time.Time
	err := s.Pool.QueryRow(ctx, `
		SELECT id::text, email, name, role, email_verified_at, storage_bytes
		FROM users WHERE id = $1::uuid`, userID).Scan(&u.ID, &u.Email, &u.Name, &role, &verified, &u.StorageBytes)
	if errors.Is(err, pgx.ErrNoRows) {
		return PublicUser{}, apperr.ErrUnauthenticated
	}
	if err != nil {
		return PublicUser{}, err
	}
	u.Role = RoleLabel(role)
	u.EmailVerified = verified != nil
	return u, nil
}

func (s *Service) CreateAdmin(ctx context.Context, email, name, password string) error {
	email = normalizeEmail(email)
	name = strings.TrimSpace(name)
	if !emailOK(email) || len([]rune(name)) < 1 || len([]rune(name)) > 50 || !PasswordOK(password) {
		return errors.New("邮箱、姓名或密码不符合要求")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), s.Cost)
	if err != nil {
		return err
	}
	id, _ := db.UUID(db.NewID())
	_, err = s.Pool.Exec(ctx, `
		INSERT INTO users (id, email, name, password_hash, role, email_verified_at, storage_bytes, created_at)
		VALUES ($1, $2, $3, $4, 'admin', now(), 0, now())`, id, email, name, string(hash))
	var pe *pgconn.PgError
	if errors.As(err, &pe) && pe.Code == "23505" {
		return errors.New("该邮箱已注册")
	}
	return err
}

func (s *Service) ResetPassword(ctx context.Context, email, password string) error {
	email = normalizeEmail(email)
	if !PasswordOK(password) {
		return errors.New("密码至少 8 位，且需同时包含字母和数字")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), s.Cost)
	if err != nil {
		return err
	}
	tag, err := s.Pool.Exec(ctx, `
		UPDATE users SET password_hash = $2, tokens_invalid_before = $3 WHERE email = $1`,
		email, string(hash), s.Clock.Now())
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errors.New("找不到这个邮箱")
	}
	return nil
}

func normalizeEmail(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

func emailOK(s string) bool {
	at := strings.Index(s, "@")
	return at > 0 && at < len(s)-1 && strings.Contains(s[at+1:], ".") && !strings.ContainsAny(s, " \t")
}
