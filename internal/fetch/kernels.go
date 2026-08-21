package fetch

import (
	"context"
	"errors"
	"strings"
)

// KernelName is a public extraction kernel name.
type KernelName string

const (
	KernelDefuddle      KernelName = "defuddle"
	KernelHTMLExtractor KernelName = "html-extractor"
	KernelLLM           KernelName = "llm"
)

// LocalKernel extracts title+markdown from HTML.
type LocalKernel func(ctx context.Context, html, pageURL string) (title, content string, err error)

// LLMKernel is an injectable llm extraction runner for this ticket.
type LLMKernel func(ctx context.Context, html, pageURL, titleHint string) (title, content string, err error)

// KernelRunners holds optional overrides. Nil local fields use package defaults.
type KernelRunners struct {
	Defuddle      LocalKernel
	HTMLExtractor LocalKernel
	LLM           LLMKernel // nil means unconfigured
}

// KernelResult is a successful extraction.
type KernelResult struct {
	Kernel  KernelName
	Title   string
	Content string
}

// ParseKernel validates an optional kernel field.
func ParseKernel(v any) (KernelName, *Error) {
	if v == nil {
		return KernelDefuddle, nil
	}
	s, ok := v.(string)
	if !ok {
		return "", NewError(CodeInvalidInput, "kernel is invalid")
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return KernelDefuddle, nil
	}
	switch KernelName(s) {
	case KernelDefuddle, KernelHTMLExtractor, KernelLLM:
		return KernelName(s), nil
	default:
		return "", NewError(CodeInvalidInput, "kernel is invalid")
	}
}

// KernelsFromSelection returns the fallback chain for a selected kernel.
func KernelsFromSelection(selected KernelName) []KernelName {
	switch selected {
	case KernelHTMLExtractor:
		return []KernelName{KernelHTMLExtractor, KernelLLM}
	case KernelLLM:
		return []KernelName{KernelLLM}
	default:
		return []KernelName{KernelDefuddle, KernelHTMLExtractor, KernelLLM}
	}
}

func normalizeMarkdown(s string) string {
	return strings.TrimSpace(s)
}

func shouldStopKernelFallback(err error, ctx context.Context) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var fe *Error
	if errors.As(err, &fe) && fe != nil {
		if fe.Code == CodeAborted || fe.Code == CodeTimeout {
			return true
		}
	}
	if ctx != nil && ctx.Err() != nil {
		return true
	}
	return false
}

func mapStopError(err error, ctx context.Context) *Error {
	if ctx != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			return NewError(CodeAborted, "fetch was aborted")
		}
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return NewError(CodeTimeout, "fetch timed out")
		}
	}
	if errors.Is(err, context.Canceled) {
		return NewError(CodeAborted, "fetch was aborted")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return NewError(CodeTimeout, "fetch timed out")
	}
	var fe *Error
	if errors.As(err, &fe) && fe != nil {
		return fe
	}
	return NewError(CodeBackendError, "extraction failed")
}

// RunKernels runs the extraction chain. html should already be Han-normalized by the caller
// or will be normalized here once.
func RunKernels(ctx context.Context, selected KernelName, html, pageURL, titleHint string, runners KernelRunners) (KernelResult, error) {
	if err := ctx.Err(); err != nil {
		return KernelResult{}, mapStopError(err, ctx)
	}
	html = NormalizeHanTypographyHTML(html)
	chain := KernelsFromSelection(selected)
	llmDirect := selected == KernelLLM
	var failures []string

	for _, kernel := range chain {
		if err := ctx.Err(); err != nil {
			return KernelResult{}, mapStopError(err, ctx)
		}

		if kernel == KernelLLM {
			if runners.LLM == nil {
				if llmDirect {
					return KernelResult{}, NewError(CodeMissingConfig, "No extract model is configured for the llm Extraction Kernel.")
				}
				failures = append(failures, "llm Extraction Kernel is not configured")
				continue
			}
			title, content, err := runners.LLM(ctx, html, pageURL, titleHint)
			if err != nil {
				if shouldStopKernelFallback(err, ctx) {
					return KernelResult{}, mapStopError(err, ctx)
				}
				if llmDirect {
					return KernelResult{}, NewError(CodeBackendError, "llm completion failed")
				}
				failures = append(failures, "llm: failed")
				continue
			}
			content = normalizeMarkdown(content)
			if content == "" {
				if llmDirect {
					return KernelResult{}, NewError(CodeBackendError, "llm returned empty markdown.")
				}
				failures = append(failures, "llm: empty markdown")
				continue
			}
			if strings.TrimSpace(title) == "" {
				title = titleHint
			}
			return KernelResult{Kernel: KernelLLM, Title: strings.TrimSpace(title), Content: content}, nil
		}

		runner := runners.Defuddle
		if kernel == KernelHTMLExtractor {
			runner = runners.HTMLExtractor
		}
		if runner == nil {
			runner = defaultLocalKernel(kernel)
		}
		title, content, err := runner(ctx, html, pageURL)
		if err != nil {
			if shouldStopKernelFallback(err, ctx) {
				return KernelResult{}, mapStopError(err, ctx)
			}
			failures = append(failures, string(kernel)+": failed")
			continue
		}
		content = normalizeMarkdown(content)
		if content == "" {
			failures = append(failures, string(kernel)+": empty markdown")
			continue
		}
		if strings.TrimSpace(title) == "" {
			title = titleHint
		}
		return KernelResult{Kernel: kernel, Title: strings.TrimSpace(title), Content: content}, nil
	}

	detail := strings.Join(failures, "; ")
	if len(detail) > 1000 {
		detail = detail[:1000] + "…"
	}
	return KernelResult{}, NewError(CodeBackendError, "No Extraction Kernel produced markdown ("+detail+").")
}

func defaultLocalKernel(name KernelName) LocalKernel {
	switch name {
	case KernelHTMLExtractor:
		return DefaultHTMLExtractor
	default:
		return DefaultDefuddle
	}
}
