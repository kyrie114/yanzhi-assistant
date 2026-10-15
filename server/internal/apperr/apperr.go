package apperr

import "errors"

// Error 是领域错误。HTTP 状态码只在 httpapi 里按 Code 映射。
type Error struct {
	Code    string
	Message string
	Details map[string]any
}

func (e *Error) Error() string { return e.Message }

func New(code, message string) *Error {
	return &Error{Code: code, Message: message}
}

func WithDetails(code, message string, details map[string]any) *Error {
	return &Error{Code: code, Message: message, Details: details}
}

func As(err error) *Error {
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	return nil
}

var (
	ErrUnauthenticated = New("UNAUTHENTICATED", "未登录或登录已过期")
	ErrCredentials     = New("INVALID_CREDENTIALS", "邮箱或密码错误")
	ErrForbidden       = New("FORBIDDEN", "没有权限执行此操作")
	ErrEmailUnverified = New("EMAIL_UNVERIFIED", "请先完成邮箱验证")
	ErrNotFound        = New("NOT_FOUND", "资源不存在")
	ErrEmailTaken      = New("EMAIL_TAKEN", "该邮箱已注册")
	ErrDuplicateDoc    = New("DUPLICATE_DOCUMENT", "资料已存在")
	ErrNotReady        = New("DOCUMENT_NOT_READY", "资料尚未完成")
	ErrNoReadyDocs     = New("NO_READY_DOCUMENTS", "请先上传资料并等待处理完成")
	ErrRetryExhausted  = New("RETRY_EXHAUSTED", "请联系管理员")
	ErrTagExists       = New("TAG_EXISTS", "标签已存在")
	ErrModelMissing    = New("MODEL_NOT_CONFIGURED", "系统尚未配置模型服务，请联系管理员")
	ErrReindexConfirm  = New("REINDEX_CONFIRMATION_REQUIRED", "更换向量模型或维度需要确认重建索引")
	ErrPasswordWeak    = New("PASSWORD_WEAK", "密码至少 8 位，且需同时包含字母和数字")
	ErrFileEncrypted   = New("FILE_ENCRYPTED", "文件已加密，无法解析")
	ErrTooManyPages    = New("TOO_MANY_PAGES", "单个文件不能超过 300 页")
	ErrQuota           = New("STORAGE_QUOTA_EXCEEDED", "存储空间不足")
	ErrFileTooLarge    = New("FILE_TOO_LARGE", "文件大小不能超过 50 MB")
	ErrUnsupported     = New("UNSUPPORTED_MEDIA", "此接口不接受该文件类型")
	ErrModelDown       = New("MODEL_UNAVAILABLE", "模型服务暂时不可用，请稍后重试")
	ErrIdempotency     = New("IDEMPOTENCY_CONFLICT", "这个幂等键已经用于另一份内容")
	ErrStreamClosed    = New("STREAM_CLOSED", "生成已结束，请从会话记录查看")
	ErrDocreaderDown   = New("DOCREADER_UNAVAILABLE", "文档解析服务暂不可用")
)

const (
	MsgNoEvidence   = "知识库中未找到相关依据"
	MsgInterrupted  = "处理中断，请重试"
	MsgEmbedDown    = "向量化服务暂不可用"
	MsgRecognize    = "识别未成功"
	MsgCorrupted    = "文件已损坏"
	MsgEncrypted    = "文件已加密，无法解析"
	MsgDocreader    = "文档解析服务暂不可用"
	QuotaBytes      = int64(2147483648)
	MaxUploadBytes  = int64(52428800)
	MaxPDFPages     = 300
)

func Quota(used int64) *Error {
	msg := "存储空间不足（已用 " + formatGB(used) + "/" + formatGB(QuotaBytes) + " GB）"
	return New("STORAGE_QUOTA_EXCEEDED", msg)
}

func formatGB(n int64) string {
	whole := n / 1073741824
	frac := (n % 1073741824) * 100 / 1073741824
	if frac == 0 {
		return itoa(whole)
	}
	return itoa(whole) + "." + pad2(frac)
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

func pad2(n int64) string {
	if n < 10 {
		return "0" + itoa(n)
	}
	return itoa(n)
}
