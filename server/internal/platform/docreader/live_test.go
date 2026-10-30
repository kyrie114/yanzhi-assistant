package docreader

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestParsePDFReadsPythonPages(t *testing.T) {
	py := os.Getenv("DOCREADER_PY")
	root := os.Getenv("DOCREADER_ROOT")
	if py == "" || root == "" {
		t.Skip("DOCREADER_PY and DOCREADER_ROOT are required")
	}
	pdfPath := filepath.Join(t.TempDir(), "two.pdf")
	write := exec.Command(py, "-c", "from test_pdf import pdf_bytes; from pathlib import Path; import os; Path(os.environ['PDF_OUT']).write_bytes(pdf_bytes(['Alpha', 'Beta']))")
	write.Dir = root
	write.Env = append(os.Environ(),
		"PDF_OUT="+pdfPath,
		"PYTHONPATH="+root+string(os.PathListSeparator)+filepath.Join(root, "tests"),
	)
	if out, err := write.CombinedOutput(); err != nil {
		t.Fatalf("write pdf: %v %s", err, out)
	}
	data, err := os.ReadFile(pdfPath)
	if err != nil {
		t.Fatal(err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(py, "-m", "docreader")
	cmd.Dir = root
	cmd.Env = append(os.Environ(),
		"DOCREADER_LISTEN="+addr,
		"DOCREADER_TOKEN=live-token",
	)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})

	client := New(addr, "live-token", 10*time.Second)
	var result Result
	deadline := time.Now().Add(8 * time.Second)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		result, err = client.ParsePDF(ctx, "doc-1", "req-1", data)
		cancel()
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal(err)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if result.ErrCode != "" || result.PageCount != 2 || !result.HasPhysicalPages {
		t.Fatalf("result %+v", result)
	}
	if len(result.Pages) != 2 || result.Pages[0].PageNo != 1 || result.Pages[1].PageNo != 2 {
		t.Fatalf("pages %+v", result.Pages)
	}
	if len(result.Pages[0].Blocks) != 1 || result.Pages[0].Blocks[0].Text != "Alpha" {
		t.Fatalf("page1 %+v", result.Pages[0])
	}
	if len(result.Pages[1].Blocks) != 1 || result.Pages[1].Blocks[0].Text != "Beta" {
		t.Fatalf("page2 %+v", result.Pages[1])
	}
}
