package prompts

import _ "embed"

//go:embed qa_system.txt
var QASystem string

//go:embed graph_extract.txt
var GraphExtract string

// Render 只替换模板里原有的 {key}。资料正文里的花括号不会再被扫描，避免死循环。
func Render(tmpl string, vars map[string]string) string {
	var b []byte
	for i := 0; i < len(tmpl); {
		if tmpl[i] == '{' {
			if j := indexByte(tmpl[i+1:], '}'); j >= 0 {
				key := tmpl[i+1 : i+1+j]
				if v, ok := vars[key]; ok {
					b = append(b, v...)
					i += j + 2
					continue
				}
			}
		}
		b = append(b, tmpl[i])
		i++
	}
	return string(b)
}

func indexByte(s string, c byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == c {
			return i
		}
	}
	return -1
}
