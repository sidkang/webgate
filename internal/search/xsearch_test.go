package search_test

import (
	"testing"

	"github.com/sidkang/webgate/internal/search"
)

func TestIsXSearchSuccessScrapedLinkIsNotEvidence(t *testing.T) {
	// Host negative case: answer-scraped x.com URL / Sources must not count.
	resp := search.BackendResponse{
		Answer: "See https://x.com/user/status/1",
		Sources: []search.Source{{
			Title: "x.com",
			URL:   "https://x.com/user/status/1",
		}},
	}
	if search.IsXSearchSuccess(resp) {
		t.Fatal("scraped Sources must not satisfy X evidence")
	}
}

func TestIsXSearchSuccessStructuredCitation(t *testing.T) {
	resp := search.BackendResponse{
		Answer:       "from a post",
		CitationURLs: []string{"https://x.com/user/status/1"},
	}
	if !search.IsXSearchSuccess(resp) {
		t.Fatal("structured citationUrls should succeed without x_search_call")
	}
	resp = search.BackendResponse{
		Answer: "from a post",
		OutputItems: []any{
			map[string]any{"type": "x_search_call"},
		},
	}
	if !search.IsXSearchSuccess(resp) {
		t.Fatal("x_search_call should succeed")
	}
}

func TestNormalizeScrapedLinkDoesNotCreateCitationURLs(t *testing.T) {
	raw := map[string]any{
		"output_text": "See https://x.com/user/status/1",
		"output":      []any{},
	}
	got := search.NormalizeBackendResponse(raw, 0)
	if got.Answer == "" {
		t.Fatal("expected answer")
	}
	if len(got.CitationURLs) != 0 {
		t.Fatalf("scraped links must not become citationUrls: %v", got.CitationURLs)
	}
	if search.IsXSearchSuccess(got) {
		t.Fatal("normalized scraped-only payload must fail IsXSearchSuccess")
	}
}
