package search_test

import (
	"context"
	"testing"
	"time"

	"github.com/sidkang/webgate/internal/search"
)

func TestIsOfficialXAIHostname(t *testing.T) {
	if !search.IsOfficialXAIHostname("api.x.ai") {
		t.Fatal("api.x.ai")
	}
	if !search.IsOfficialXAIHostname("proxy.api.x.ai") {
		t.Fatal("subdomain")
	}
	if search.IsOfficialXAIHostname("cliproxy.example.com") {
		t.Fatal("proxy should be allowed")
	}
}

func TestCleanedLocation(t *testing.T) {
	loc := search.CleanedLocation(&search.UserLocation{Country: "China", City: "Shanghai"})
	if _, ok := loc["country"]; ok {
		t.Fatalf("China kept: %v", loc)
	}
	if loc["city"] != "Shanghai" || loc["type"] != "approximate" {
		t.Fatalf("%v", loc)
	}
	loc = search.CleanedLocation(&search.UserLocation{Country: "cn"})
	if loc["country"] != "CN" {
		t.Fatalf("%v", loc)
	}
}

func TestHasWebSearchSuccess(t *testing.T) {
	if search.HasWebSearchSuccess(search.BackendResponse{}) {
		t.Fatal("empty")
	}
	if search.HasWebSearchSuccess(search.BackendResponse{Answer: "a"}) {
		t.Fatal("answer-only must fail without structured evidence")
	}
	if search.HasWebSearchSuccess(search.BackendResponse{
		Answer:  "a",
		Sources: []search.Source{{URL: "https://a.com"}},
	}) {
		t.Fatal("scraped sources alone are not enough")
	}
	if !search.HasWebSearchSuccess(search.BackendResponse{
		Answer:       "a",
		CitationURLs: []string{"https://a.com"},
	}) {
		t.Fatal("citation urls")
	}
	if !search.HasWebSearchSuccess(search.BackendResponse{
		Answer: "a",
		OutputItems: []any{
			map[string]any{"type": "web_search_call"},
		},
	}) {
		t.Fatal("web_search_call")
	}
}

func TestResponsesClientTimeoutClassified(t *testing.T) {
	client := &search.ResponsesClient{BaseURL: "http://127.0.0.1:1", APIKey: "k"}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := client.Post(ctx, map[string]any{"model": "m"}, 5)
	if err == nil {
		t.Fatal("expected error")
	}
	classified := search.Classify(err)
	if classified.Code != search.CodeTimeout && classified.Code != search.CodeBackendError {
		t.Fatalf("classified=%+v err=%v", classified, err)
	}
}
