package provider

import "fmt"

// ErrorKind 请求错误的分类，供调用方做针对性处理（重试、降级、提示用户等）。
type ErrorKind int

const (
	KindUnknown   ErrorKind = iota // 未分类
	KindNetwork                    // 网络错误（连接失败、连接被重置等）
	KindTimeout                    // 超时（含 context deadline）
	KindCanceled                   // context 取消
	KindAuth                       // 401/403 认证错误
	KindRateLimit                  // 429 限流
	KindRequest                    // 其他 4xx 请求错误
	KindServer                     // 5xx 服务端错误
	KindStream                     // 流式响应解析错误
)

// Error 是 provider 层返回的分类错误；用 errors.As 取出后按 Kind 判断。
type Error struct {
	Kind       ErrorKind // 错误分类
	StatusCode int       // HTTP 状态码，未收到响应时为 0
	Message    string    // 可读描述
	Retryable  bool      // 是否适合重试
}

// Error 实现 error 接口。
func (e *Error) Error() string {
	switch {
	case e.StatusCode > 0:
		return fmt.Sprintf("provider: %s (HTTP %d)", e.Message, e.StatusCode)
	default:
		return fmt.Sprintf("provider: %s", e.Message)
	}
}

// classifyStatus 按 HTTP 状态码构造分类错误。
func classifyStatus(code int, msg string) *Error {
	e := &Error{StatusCode: code, Message: msg}
	switch {
	case code == 401 || code == 403:
		e.Kind = KindAuth
	case code == 429:
		e.Kind, e.Retryable = KindRateLimit, true
	case code >= 500:
		e.Kind, e.Retryable = KindServer, true
	default:
		e.Kind = KindRequest
	}
	return e
}
