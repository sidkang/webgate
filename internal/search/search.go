package search

import (
	"context"
	"strings"
)

type Source struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet"`
}

type Result struct {
	Answer  string
	Sources []Source
}

// UserLocation is the optional web_search user_location payload.
type UserLocation struct {
	Type     string `json:"type,omitempty"`
	Country  string `json:"country,omitempty"`
	Region   string `json:"region,omitempty"`
	City     string `json:"city,omitempty"`
	Timezone string `json:"timezone,omitempty"`
}

// Request is the shared search source request.
type Request struct {
	Query             string
	Limit             int
	SearchContextSize string // low|medium|high; empty → high for OpenAI
	AllowedDomains    []string
	UserLocation      *UserLocation
}

type Searcher interface {
	Search(ctx context.Context, req Request) (Result, error)
}

// Fixed is a test double that always returns Result.
type Fixed struct {
	Result Result
	Err    error
}

func (f Fixed) Search(context.Context, Request) (Result, error) {
	return f.Result, f.Err
}

// Func adapts a function to Searcher. Extra Request fields are ignored.
type Func func(query string, limit int) (Result, error)

func (f Func) Search(_ context.Context, req Request) (Result, error) {
	return f(req.Query, req.Limit)
}

// RequestFunc adapts a full Request-aware function to Searcher.
type RequestFunc func(ctx context.Context, req Request) (Result, error)

func (f RequestFunc) Search(ctx context.Context, req Request) (Result, error) {
	return f(ctx, req)
}

func IsEmpty(r Result) bool {
	if strings.TrimSpace(r.Answer) != "" {
		return false
	}
	for _, src := range r.Sources {
		if strings.TrimSpace(src.URL) != "" {
			return false
		}
	}
	return true
}

// ParseSearchContextSize validates an optional search_context_size value.
// Empty / omitted → "" (OpenAI treats empty as high when building).
func ParseSearchContextSize(v any) (string, *Error) {
	if v == nil {
		return "", nil
	}
	s, ok := v.(string)
	if !ok {
		return "", NewError(CodeInvalidInput, "search_context_size is invalid")
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return "", nil
	}
	switch s {
	case "low", "medium", "high":
		return s, nil
	default:
		return "", NewError(CodeInvalidInput, "search_context_size is invalid")
	}
}
