package knowledge

import "yanzhi/server/internal/apperr"

type Existing struct {
	ID     string
	KBID   string
	Status string
}

type UploadInput struct {
	KBOwned       bool
	Size          int64
	IsPDF         bool
	Encrypted     bool
	PageCount     *int
	StorageBytes  int64
	Existing      *Existing
}

type UploadOutcome struct {
	Code     int
	Err      *apperr.Error
	Reuse    bool
	Existing *Existing
}

// DecideUpload 按接口设计第 6 节的固定顺序给出结果。调用方已处理 401 和邮箱 403。
func DecideUpload(in UploadInput) UploadOutcome {
	if !in.KBOwned {
		return UploadOutcome{Code: 404, Err: apperr.ErrNotFound}
	}
	if in.Size > apperr.MaxUploadBytes {
		return UploadOutcome{Code: 413, Err: apperr.ErrFileTooLarge}
	}
	if !in.IsPDF {
		return UploadOutcome{Code: 415, Err: apperr.ErrUnsupported}
	}
	if in.Encrypted {
		return UploadOutcome{Code: 422, Err: apperr.ErrFileEncrypted}
	}
	if in.PageCount != nil && *in.PageCount > apperr.MaxPDFPages {
		return UploadOutcome{Code: 422, Err: apperr.ErrTooManyPages}
	}
	if in.StorageBytes+in.Size > apperr.QuotaBytes {
		return UploadOutcome{Code: 422, Err: apperr.Quota(in.StorageBytes)}
	}
	if in.Existing != nil {
		if in.Existing.Status == "failed" {
			return UploadOutcome{Code: 200, Reuse: true, Existing: in.Existing}
		}
		return UploadOutcome{Code: 409, Err: apperr.WithDetails("DUPLICATE_DOCUMENT", "资料已存在", map[string]any{
			"document_id": in.Existing.ID,
			"kb_id":       in.Existing.KBID,
		})}
	}
	return UploadOutcome{Code: 201}
}

func NameOK(name string) bool {
	n := len([]rune(name))
	return n >= 1 && n <= 50
}
