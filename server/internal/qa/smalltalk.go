package qa

import "strings"

var smalltalk = map[string]struct{}{
	"你好": {}, "您好": {}, "嗨": {}, "哈喽": {}, "在吗": {},
	"早上好": {}, "晚上好": {}, "谢谢": {}, "谢谢你": {}, "感谢": {}, "多谢": {},
	"再见": {}, "拜拜": {},
	"hi": {}, "hello": {}, "hey": {},
	"thanks": {}, "thank you": {}, "bye": {}, "goodbye": {},
}

func IsSmalltalk(q string) bool {
	s := strings.TrimSpace(q)
	s = strings.Trim(s, "。.!！?？~，, ")
	s = strings.ToLower(s)
	if len([]rune(s)) > 12 {
		return false
	}
	_, ok := smalltalk[s]
	return ok
}

func AnswerLanguage(q string) string {
	han := 0
	for _, r := range q {
		if r >= 0x4E00 && r <= 0x9FFF {
			han++
		}
	}
	if han > 0 {
		return "简体中文"
	}
	if len([]rune(strings.TrimSpace(q))) <= 4 {
		return "简体中文"
	}
	return "与问题相同的语言"
}
