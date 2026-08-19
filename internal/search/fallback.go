package search

import (
	"context"
	"errors"
	"net"
	"net/url"
	"strings"
)

// FallbackKind says whether sequential resolution should try the next source.
type FallbackKind string

const (
	KindContinue FallbackKind = "continue"
	KindStop     FallbackKind = "stop"
)

var continueCodes = map[Code]struct{}{
	CodeRateLimited:     {},
	CodeBackendError:    {},
	CodeTimeout:         {},
	CodeInvalidResponse: {},
}

var stopCodes = map[Code]struct{}{
	CodeAborted:               {},
	CodeInvalidInput:          {},
	CodeAuthFailed:            {},
	CodeMissingConfig:         {},
	CodeUnsupportedModel:      {},
	CodeUnsupportedTool:       {},
	CodeUnsupportedToolChoice: {},
}

// Classified is the fallback decision for a source failure.
type Classified struct {
	Kind    FallbackKind
	Code    Code
	Message string
}

// Classify maps an error to continue/stop and a stable code.
func Classify(err error) Classified {
	if err == nil {
		return Classified{Kind: KindStop, Code: CodeBackendError, Message: "backend error"}
	}

	var se *Error
	if errors.As(err, &se) && se != nil {
		return classifyCode(se.Code, se.Message)
	}

	if errors.Is(err, context.Canceled) {
		return Classified{Kind: KindStop, Code: CodeAborted, Message: "web search was aborted"}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return Classified{Kind: KindContinue, Code: CodeTimeout, Message: "web search timed out"}
	}
	if isTimeoutNetworkError(err) {
		return Classified{Kind: KindContinue, Code: CodeTimeout, Message: "web search timed out"}
	}
	if isNetworkError(err) {
		return Classified{Kind: KindContinue, Code: CodeBackendError, Message: "network error"}
	}

	// Unexpected errors stop the chain (host treats generic Error as stop).
	return Classified{Kind: KindStop, Code: CodeBackendError, Message: "backend error"}
}

func classifyCode(code Code, message string) Classified {
	if message == "" {
		message = string(code)
	}
	if _, ok := continueCodes[code]; ok {
		return Classified{Kind: KindContinue, Code: code, Message: message}
	}
	if _, ok := stopCodes[code]; ok {
		return Classified{Kind: KindStop, Code: code, Message: message}
	}
	return Classified{Kind: KindStop, Code: code, Message: message}
}

// isTimeoutNetworkError is true when err is (or wraps) a net.Error with Timeout().
func isTimeoutNetworkError(err error) bool {
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return true
	}
	return false
}

func isNetworkError(err error) bool {
	if err == nil {
		return false
	}
	var ne net.Error
	if errors.As(err, &ne) {
		return true
	}
	var dns *net.DNSError
	if errors.As(err, &dns) {
		return true
	}
	var op *net.OpError
	if errors.As(err, &op) {
		return true
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		return isNetworkError(ue.Err)
	}
	msg := strings.ToLower(err.Error())
	needles := []string{
		"connection refused",
		"connection reset",
		"no such host",
		"i/o timeout",
		"network is unreachable",
		"tls handshake timeout",
		"temporary failure in name resolution",
	}
	for _, n := range needles {
		if strings.Contains(msg, n) {
			return true
		}
	}
	return false
}

// SearchChain runs sources in order. When skipMissing is true, nil sources are dropped
// before the first call; if none remain, missing_config is returned. When skipMissing is
// false, a nil source fails immediately with missing_config (no later sources).
// On continue failures the next source is tried; the last classified error is returned
// when the chain is exhausted. On stop failures the error is returned immediately.
func SearchChain(ctx context.Context, sources map[string]Searcher, chain []string, req Request, skipMissing bool) (Result, string, *Error) {
	names := chain
	if skipMissing {
		filtered := make([]string, 0, len(chain))
		for _, name := range chain {
			if sources[name] != nil {
				filtered = append(filtered, name)
			}
		}
		names = filtered
		if len(names) == 0 {
			return Result{}, "", NewError(CodeMissingConfig, "no search source is configured")
		}
	}

	var last *Error
	for _, name := range names {
		src := sources[name]
		if src == nil {
			return Result{}, "", NewError(CodeMissingConfig, name+" is not configured")
		}

		result, err := src.Search(ctx, req)
		if err == nil {
			if !IsEmpty(result) {
				return result, name, nil
			}
			last = NewError(CodeInvalidResponse, "search returned no answer or sources")
			// Empty shell continues to the next source when fallback is allowed.
			continue
		}

		classified := Classify(err)
		last = NewError(classified.Code, classified.Message)
		if classified.Kind == KindStop {
			return Result{}, "", last
		}
	}
	if last == nil {
		return Result{}, "", NewError(CodeMissingConfig, "no search source is configured")
	}
	return Result{}, "", last
}
