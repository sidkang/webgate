package search

import "context"

type Source struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet"`
}

type Result struct {
	Answer  string
	Sources []Source
}

type Searcher interface {
	Search(ctx context.Context, query string, limit int) (Result, error)
}

// Fixed is a test double that always returns Result.
type Fixed struct {
	Result Result
	Err    error
}

func (f Fixed) Search(context.Context, string, int) (Result, error) {
	return f.Result, f.Err
}

// Func adapts a function to Searcher.
type Func func(query string, limit int) (Result, error)

func (f Func) Search(_ context.Context, query string, limit int) (Result, error) {
	return f(query, limit)
}

func IsEmpty(r Result) bool {
	if len(r.Sources) > 0 {
		return false
	}
	return r.Answer == ""
}
