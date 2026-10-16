package account

import "unicode"

// ValidPassword 要求至少 8 个字符，且同时含字母和数字。
func ValidPassword(password string) bool {
	if len([]rune(password)) < 8 {
		return false
	}
	var letter, digit bool
	for _, r := range password {
		if unicode.IsLetter(r) {
			letter = true
		}
		if unicode.IsDigit(r) {
			digit = true
		}
	}
	return letter && digit
}
