package knowledge

import "testing"

func TestUploadOrder(t *testing.T) {
	pages := 350
	cases := []struct {
		name string
		in   UploadInput
		code int
	}{
		{"foreign kb", UploadInput{Size: 10, IsPDF: true}, 404},
		{"too big before type", UploadInput{KBOwned: true, Size: 52428801, IsPDF: false}, 413},
		{"docx", UploadInput{KBOwned: true, Size: 100, IsPDF: false}, 415},
		{"encrypted", UploadInput{KBOwned: true, Size: 100, IsPDF: true, Encrypted: true}, 422},
		{"pages", UploadInput{KBOwned: true, Size: 100, IsPDF: true, PageCount: &pages}, 422},
		{"exact size", UploadInput{KBOwned: true, Size: 52428800, IsPDF: true}, 201},
		{"quota", UploadInput{KBOwned: true, Size: 20971520, IsPDF: true, StorageBytes: 2137000000}, 422},
		{"dup", UploadInput{KBOwned: true, Size: 10, IsPDF: true, Existing: &Existing{ID: "d", KBID: "k", Status: "ready"}}, 409},
		{"failed reuse", UploadInput{KBOwned: true, Size: 10, IsPDF: true, Existing: &Existing{Status: "failed"}}, 200},
	}
	for _, tc := range cases {
		got := DecideUpload(tc.in)
		if got.Code != tc.code {
			t.Fatalf("%s: got %d want %d", tc.name, got.Code, tc.code)
		}
	}
	if !NameOK("论文阅读") || NameOK("") || NameOK(string(make([]rune, 51))) {
		t.Fatal("name")
	}
}
