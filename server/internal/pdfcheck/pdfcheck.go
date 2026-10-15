package pdfcheck

import (
	"bytes"
	"regexp"
)

type Info struct {
	IsPDF      bool
	Encrypted  bool
	PageCount  *int
}

var pageRe = regexp.MustCompile(`/Type\s*/Page(?:[^s]|$)`)

// Inspect 只看文件头、加密标记和页对象，不调用 docreader。
func Inspect(data []byte) Info {
	trimmed := bytes.TrimLeft(data, " \t\r\n")
	info := Info{IsPDF: bytes.HasPrefix(trimmed, []byte("%PDF"))}
	if !info.IsPDF {
		return info
	}
	info.Encrypted = bytes.Contains(data, []byte("/Encrypt"))
	matches := pageRe.FindAll(data, -1)
	if len(matches) > 0 {
		n := len(matches)
		info.PageCount = &n
	}
	return info
}
