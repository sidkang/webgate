package fetch

import (
	"context"
	"errors"
	"strings"
	"time"
)

// Mode is the fetch response mode.
type Mode string

const (
	ModeReadable Mode = "readable"
	ModeRaw      Mode = "raw"
)

// Result is the successful fetch response payload (before HTTP encoding).
type Result struct {
	URL           string
	RequestedURL  string
	Title         string
	Content       string
	Mode          Mode
	Kernel        string
	Truncated     bool
	TotalChars    int
	ReturnedChars int
}

// ServiceConfig configures a fetch Service.
type ServiceConfig struct {
	CloakDisabled  bool
	CDPEndpoint    string
	CDPAPIKey      string
	Capturer       Capturer
	Kernels        KernelRunners
	MaxInlineChars int
	FetchTimeout   time.Duration
	Lookup         Lookup
}

// Service runs fetch acquisition + extraction.
type Service struct {
	cloakDisabled bool
	endpoint      string
	apiKey        string
	capturer      Capturer
	kernels       KernelRunners
	maxInline     int
	timeout       time.Duration
	lookup        Lookup
}

func NewService(cfg ServiceConfig) *Service {
	timeout := cfg.FetchTimeout
	if timeout <= 0 {
		timeout = time.Duration(FetchTimeoutMS) * time.Millisecond
	}
	capturer := cfg.Capturer
	if capturer == nil && strings.TrimSpace(cfg.CDPEndpoint) != "" {
		capturer = CDPCapturer{
			Endpoint: cfg.CDPEndpoint,
			APIKey:   cfg.CDPAPIKey,
			Lookup:   cfg.Lookup,
		}
	}
	return &Service{
		cloakDisabled: cfg.CloakDisabled,
		endpoint:      strings.TrimSpace(cfg.CDPEndpoint),
		apiKey:        cfg.CDPAPIKey,
		capturer:      capturer,
		kernels:       cfg.Kernels,
		maxInline:     NormalizeMaxInlineChars(cfg.MaxInlineChars),
		timeout:       timeout,
		lookup:        cfg.Lookup,
	}
}

// ParseMode validates an optional mode field.
func ParseMode(v any) (Mode, *Error) {
	if v == nil {
		return ModeReadable, nil
	}
	s, ok := v.(string)
	if !ok {
		return "", NewError(CodeInvalidInput, "mode is invalid")
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return ModeReadable, nil
	}
	switch Mode(s) {
	case ModeReadable, ModeRaw:
		return Mode(s), nil
	default:
		return "", NewError(CodeInvalidInput, "mode is invalid")
	}
}

// Fetch acquires a page and optionally extracts readable content.
func (s *Service) Fetch(ctx context.Context, rawURL string, mode Mode, kernel KernelName) (Result, error) {
	if s.cloakDisabled {
		return Result{}, NewError(CodeCloakDisabled, "Cloak browser access is disabled")
	}
	if s.capturer == nil {
		return Result{}, NewError(CodeMissingConfig, "No CDP endpoint is configured. Set CDP_ENDPOINT.")
	}

	if _, err := AssertFetchURLAllowed(ctx, rawURL, s.lookup); err != nil {
		return Result{}, err
	}

	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	page, err := s.capturer.Capture(ctx, strings.TrimSpace(rawURL))
	if err != nil {
		return Result{}, mapFetchErr(err, ctx)
	}
	requested := page.RequestedURL
	if requested == "" {
		requested = strings.TrimSpace(rawURL)
	}
	finalURL := page.URL
	if finalURL == "" {
		finalURL = requested
	}

	var (
		content   string
		title     string
		kernelOut string
	)
	if mode == ModeRaw {
		content = page.HTML
		title = page.Title
		kernelOut = ""
	} else {
		extracted, kerr := RunKernels(ctx, kernel, page.HTML, finalURL, page.Title, s.kernels)
		if kerr != nil {
			return Result{}, mapFetchErr(kerr, ctx)
		}
		content = extracted.Content
		title = extracted.Title
		if title == "" {
			title = page.Title
		}
		kernelOut = string(extracted.Kernel)
	}

	slice := InitialContentSlice(content, s.maxInline)
	text := slice.Text
	if slice.Truncated {
		text += TruncationNotice(slice.ReturnedChars, slice.TotalChars)
	}
	return Result{
		URL:           finalURL,
		RequestedURL:  requested,
		Title:         title,
		Content:       text,
		Mode:          mode,
		Kernel:        kernelOut,
		Truncated:     slice.Truncated,
		TotalChars:    slice.TotalChars,
		ReturnedChars: slice.ReturnedChars,
	}, nil
}

func mapFetchErr(err error, ctx context.Context) error {
	if err == nil {
		return nil
	}
	var fe *Error
	if errors.As(err, &fe) {
		return fe
	}
	if ctx != nil && ctx.Err() != nil {
		return mapCtxErr(ctx.Err())
	}
	return NewError(CodeBackendError, "fetch failed")
}
