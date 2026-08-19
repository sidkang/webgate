package server_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sidkang/webgate/internal/search"
	"github.com/sidkang/webgate/internal/server"
)

const testToken = "test-token"

func postSearch(t *testing.T, h http.Handler, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/search", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func decode(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("json: %v body=%s", err, rec.Body.String())
	}
	return out
}

func TestSearchRequiresBearerToken(t *testing.T) {
	h := server.New(server.Config{Token: testToken, Searcher: search.Fixed{}})
	rec := postSearch(t, h, "", map[string]any{"query": "go", "provider": "searxng"})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if decode(t, rec)["code"] != "auth_failed" {
		t.Fatalf("body=%s", rec.Body.String())
	}
}

func TestSearchRejectsWrongToken(t *testing.T) {
	h := server.New(server.Config{Token: testToken, Searcher: search.Fixed{}})
	rec := postSearch(t, h, "nope", map[string]any{"query": "go", "provider": "searxng"})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if decode(t, rec)["code"] != "auth_failed" {
		t.Fatalf("body=%s", rec.Body.String())
	}
}

func TestSearchSearXNGReturnsNormalizedResults(t *testing.T) {
	h := server.New(server.Config{
		Token: testToken,
		Searcher: search.Fixed{Result: search.Result{
			Answer: "Go is a language.",
			Sources: []search.Source{{
				Title:   "Go",
				URL:     "https://go.dev",
				Snippet: "Go is a language.",
			}},
		}},
	})
	rec := postSearch(t, h, testToken, map[string]any{
		"query":    "golang",
		"limit":    5,
		"provider": "searxng",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	out := decode(t, rec)
	if out["answer"] != "Go is a language." {
		t.Fatalf("answer=%v", out["answer"])
	}
	if out["provider"] != "searxng" {
		t.Fatalf("provider=%v", out["provider"])
	}
	sources, ok := out["sources"].([]any)
	if !ok || len(sources) != 1 {
		t.Fatalf("sources=%v", out["sources"])
	}
	src := sources[0].(map[string]any)
	if src["url"] != "https://go.dev" || src["title"] != "Go" {
		t.Fatalf("source=%v", src)
	}
}

func TestSearchEmptyShellFails(t *testing.T) {
	h := server.New(server.Config{Token: testToken, Searcher: search.Fixed{Result: search.Result{}}})
	rec := postSearch(t, h, testToken, map[string]any{"query": "nothing", "provider": "searxng"})
	if rec.Code == http.StatusOK {
		t.Fatalf("expected failure, body=%s", rec.Body.String())
	}
	out := decode(t, rec)
	if out["code"] != "invalid_response" {
		t.Fatalf("code=%v body=%s", out["code"], rec.Body.String())
	}
}

func TestSearchSourceWithoutURLIsEmpty(t *testing.T) {
	h := server.New(server.Config{
		Token: testToken,
		Searcher: search.Fixed{Result: search.Result{
			Sources: []search.Source{{Title: "no url", Snippet: "x"}},
		}},
	})
	rec := postSearch(t, h, testToken, map[string]any{"query": "nothing", "provider": "searxng"})
	out := decode(t, rec)
	if rec.Code == http.StatusOK || out["code"] != "invalid_response" {
		t.Fatalf("status=%d code=%v body=%s", rec.Code, out["code"], rec.Body.String())
	}
}

func TestSearchInvalidLimit(t *testing.T) {
	h := server.New(server.Config{Token: testToken, Searcher: search.Fixed{}})
	for _, limit := range []any{0, 21, -1} {
		rec := postSearch(t, h, testToken, map[string]any{"query": "go", "limit": limit, "provider": "searxng"})
		if rec.Code == http.StatusOK {
			t.Fatalf("limit %v should fail", limit)
		}
		out := decode(t, rec)
		if out["code"] != "invalid_input" {
			t.Fatalf("limit %v code=%v", limit, out["code"])
		}
	}
}

func TestSearchMissingSearXNGConfig(t *testing.T) {
	h := server.New(server.Config{Token: testToken})
	rec := postSearch(t, h, testToken, map[string]any{"query": "go", "provider": "searxng"})
	if rec.Code == http.StatusOK {
		t.Fatalf("expected missing_config, body=%s", rec.Body.String())
	}
	out := decode(t, rec)
	if out["code"] != "missing_config" {
		t.Fatalf("code=%v body=%s", out["code"], rec.Body.String())
	}
}

func TestSearchThroughFakeSearXNG(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/search" || r.URL.Query().Get("format") != "json" {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("q") != "rust" {
			t.Errorf("query=%q", r.URL.Query().Get("q"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"results": []map[string]string{{
				"title":   "Rust",
				"url":     "https://www.rust-lang.org",
				"content": "A language empowering everyone.",
			}},
		})
	}))
	t.Cleanup(upstream.Close)

	h := server.New(server.Config{
		Token:    testToken,
		Searcher: search.SearXNG{BaseURL: upstream.URL, HTTPClient: upstream.Client()},
	})
	rec := postSearch(t, h, testToken, map[string]any{"query": "rust", "limit": 5, "provider": "searxng"})
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	out := decode(t, rec)
	sources := out["sources"].([]any)
	if len(sources) != 1 {
		t.Fatalf("sources=%v", out["sources"])
	}
	if sources[0].(map[string]any)["url"] != "https://www.rust-lang.org" {
		t.Fatalf("source=%v", sources[0])
	}
}

func TestSearchHonorsLimit(t *testing.T) {
	var gotLimit int
	h := server.New(server.Config{
		Token: testToken,
		Searcher: search.Func(func(_ string, limit int) (search.Result, error) {
			gotLimit = limit
			return search.Result{
				Sources: []search.Source{{Title: "A", URL: "https://a.example", Snippet: "a"}},
			}, nil
		}),
	})
	rec := postSearch(t, h, testToken, map[string]any{"query": "go", "limit": 3, "provider": "searxng"})
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if gotLimit != 3 {
		t.Fatalf("limit=%d", gotLimit)
	}
}
