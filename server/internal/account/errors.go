package account

import "errors"

var (
	// ErrEmailTaken 对应 409 EMAIL_TAKEN。
	ErrEmailTaken = errors.New("email taken")
	// ErrWeakPassword 对应 422 PASSWORD_WEAK。
	ErrWeakPassword = errors.New("weak password")
	// ErrInvalidCredentials 对应 401，邮箱不存在和密码错误使用同一句提示。
	ErrInvalidCredentials = errors.New("invalid credentials")
	// ErrUnauthenticated 对应 401，未登录、令牌过期、已退出或已被改密码作废。
	ErrUnauthenticated = errors.New("unauthenticated")
)

// ValidationError 对应 422 VALIDATION_ERROR，说明放在字段上。
type ValidationError struct {
	Details map[string]string
}

func (e *ValidationError) Error() string { return "validation" }
