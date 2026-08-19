package search

// Code is a stable search error code returned to clients (and used for fallback).
type Code string

const (
	CodeInvalidInput          Code = "invalid_input"
	CodeMissingConfig         Code = "missing_config"
	CodeAuthFailed            Code = "auth_failed"
	CodeBackendError          Code = "backend_error"
	CodeInvalidResponse       Code = "invalid_response"
	CodeRateLimited           Code = "rate_limited"
	CodeTimeout               Code = "timeout"
	CodeAborted               Code = "aborted"
	CodeUnsupportedModel      Code = "unsupported_model"
	CodeUnsupportedTool       Code = "unsupported_tool"
	CodeUnsupportedToolChoice Code = "unsupported_tool_choice"
)

// Error is a typed search failure. Message is internal; HTTP handlers map Code to a public phrase.
type Error struct {
	Code    Code
	Message string
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	if e.Message != "" {
		return e.Message
	}
	return string(e.Code)
}

// NewError builds a typed search error.
func NewError(code Code, message string) *Error {
	return &Error{Code: code, Message: message}
}
