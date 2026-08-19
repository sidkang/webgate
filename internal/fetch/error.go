package fetch

// Code is a stable fetch/API error code.
type Code string

const (
	CodeInvalidInput   Code = "invalid_input"
	CodeMissingConfig  Code = "missing_config"
	CodeBackendError   Code = "backend_error"
	CodeTimeout        Code = "timeout"
	CodeAborted        Code = "aborted"
	CodeCloakDisabled  Code = "cloak_disabled"
)

// Error is a typed fetch failure. Message is internal; handlers map Code to a public phrase.
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

func NewError(code Code, message string) *Error {
	return &Error{Code: code, Message: message}
}
