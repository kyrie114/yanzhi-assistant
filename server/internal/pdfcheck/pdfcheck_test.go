package pdfcheck

import "testing"

func TestInspectHeaderAndPages(t *testing.T) {
	plain := []byte("%PDF-1.4\n/Type /Pages /Count 2\n/Type /Page\n/Type /Page\n")
	info := Inspect(plain)
	if !info.IsPDF || info.Encrypted || info.PageCount == nil || *info.PageCount != 2 {
		t.Fatalf("%+v", info)
	}
	enc := append([]byte("%PDF-1.7\n"), []byte("/Encrypt 5 0 R\n/Type /Page\n")...)
	info = Inspect(enc)
	if !info.Encrypted {
		t.Fatal("expected encrypted")
	}
	docx := []byte("PK\x03\x04 not a pdf")
	info = Inspect(docx)
	if info.IsPDF {
		t.Fatal("docx is not pdf")
	}
}
