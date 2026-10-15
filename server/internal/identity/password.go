package identity

import (
	"time"
	"unicode"
)

func PasswordOK(p string) bool {
	if len([]rune(p)) < 8 {
		return false
	}
	letter, digit := false, false
	for _, r := range p {
		if unicode.IsLetter(r) {
			letter = true
		}
		if unicode.IsDigit(r) {
			digit = true
		}
	}
	return letter && digit
}

// TokenActive：签名和过期由调用方先查。这里只看作废表和改密码时间。
// 有效条件是 iat 不早于 tokens_invalid_before。
func TokenActive(iat, exp, now time.Time, invalidBefore *time.Time, revoked bool) bool {
	if revoked || !exp.After(now) {
		return false
	}
	if invalidBefore != nil && iat.Before(*invalidBefore) {
		return false
	}
	return true
}

func RoleLabel(role string) string {
	if role == "admin" {
		return "系统管理员"
	}
	return "注册用户"
}

func DocStatusLabel(code string) string {
	switch code {
	case "pending":
		return "待处理"
	case "parsing":
		return "解析中"
	case "embedding":
		return "向量化中"
	case "ready":
		return "已完成"
	case "failed":
		return "失败"
	default:
		return code
	}
}

func GraphStatusLabel(code string) any {
	if code == "" {
		return nil
	}
	switch code {
	case "pending":
		return "待抽取"
	case "extracting":
		return "抽取中"
	case "ready":
		return "已完成"
	case "failed":
		return "失败"
	default:
		return code
	}
}
