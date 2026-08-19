package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
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

func sources(m map[string]search.Searcher) server.Config {
	return server.Config{Token: testToken, Sources: m}
}

func okResult(answer string) search.Result {
	return search.Result{
		Answer:  answer,
		Sources: []search.Source{{Title: "T", URL: "https://example.com", Snippet: answer}},
	}
}

type callRecorder struct {
	mu    sync.Mutex
	order []string
}

func (c *callRecorder) track(name string, next search.Searcher) search.Searcher {
	return search.Func(func(query string, limit int) (search.Result, error) {
		c.mu.Lock()
		c.order = append(c.order, name)
		c.mu.Unlock()
		return next.Search(context.Background(), query, limit)
	})
}

func (c *callRecorder) snapshot() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, len(c.order))
	copy(out, c.order)
	return out
}

func TestSearchRequiresBearerToken(t *testing.T) {
	h := server.New(sources(map[string]search.Searcher{"searxng": search.Fixed{}}))
	rec := postSearch(t, h, "", map[string]any{"query": "go", "provider": "searxng"})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if decode(t, rec)["code"] != "auth_failed" {
		t.Fatalf("body=%s", rec.Body.String())
	}
}

func TestSearchRejectsWrongToken(t *testing.T) {
	h := server.New(sources(map[string]search.Searcher{"searxng": search.Fixed{}}))
	rec := postSearch(t, h, "nope", map[string]any{"query": "go", "provider": "searxng"})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if decode(t, rec)["code"] != "auth_failed" {
		t.Fatalf("body=%s", rec.Body.String())
	}
}

func TestSearchSearXNGReturnsNormalizedResults(t *testing.T) {
	h := server.New(sources(map[string]search.Searcher{
		"searxng": search.Fixed{Result: search.Result{
			Answer: "Go is a language.",
			Sources: []search.Source{{
				Title:   "Go",
				URL:     "https://go.dev",
				Snippet: "Go is a language.",
			}},
		}},
	}))
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
	srcs, ok := out["sources"].([]any)
	if !ok || len(srcs) != 1 {
		t.Fatalf("sources=%v", out["sources"])
	}
	src := srcs[0].(map[string]any)
	if src["url"] != "https://go.dev" || src["title"] != "Go" {
		t.Fatalf("source=%v", src)
	}
}

func TestSearchEmptyShellFails(t *testing.T) {
	h := server.New(sources(map[string]search.Searcher{"searxng": search.Fixed{Result: search.Result{}}}))
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
	h := server.New(sources(map[string]search.Searcher{
		"searxng": search.Fixed{Result: search.Result{
			Sources: []search.Source{{Title: "no url", Snippet: "x"}},
		}},
	}))
	rec := postSearch(t, h, testToken, map[string]any{"query": "nothing", "provider": "searxng"})
	out := decode(t, rec)
	if rec.Code == http.StatusOK || out["code"] != "invalid_response" {
		t.Fatalf("status=%d code=%v body=%s", rec.Code, out["code"], rec.Body.String())
	}
}

func TestSearchInvalidLimit(t *testing.T) {
	h := server.New(sources(map[string]search.Searcher{"searxng": search.Fixed{}}))
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

	h := server.New(sources(map[string]search.Searcher{
		"searxng": search.SearXNG{BaseURL: upstream.URL, HTTPClient: upstream.Client()},
	}))
	rec := postSearch(t, h, testToken, map[string]any{"query": "rust", "limit": 5, "provider": "searxng"})
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	out := decode(t, rec)
	got := out["sources"].([]any)
	if len(got) != 1 {
		t.Fatalf("sources=%v", out["sources"])
	}
	if got[0].(map[string]any)["url"] != "https://www.rust-lang.org" {
		t.Fatalf("source=%v", got[0])
	}
}

func TestSearchHonorsLimit(t *testing.T) {
	var gotLimit int
	h := server.New(sources(map[string]search.Searcher{
		"searxng": search.Func(func(_ string, limit int) (search.Result, error) {
			gotLimit = limit
			return search.Result{
				Sources: []search.Source{{Title: "A", URL: "https://a.example", Snippet: "a"}},
			}, nil
		}),
	}))
	rec := postSearch(t, h, testToken, map[string]any{"query": "go", "limit": 3, "provider": "searxng"})
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if gotLimit != 3 {
		t.Fatalf("limit=%d", gotLimit)
	}
}

func TestOmitProviderMatchesLLMChain(t *testing.T) {
	rec := &callRecorder{}
	h := server.New(sources(map[string]search.Searcher{
		"openai": rec.track("openai", search.Fixed{Err: search.NewError(search.CodeBackendError, "openai down")}),
		"xai":    rec.track("xai", search.Fixed{Result: okResult("from xai")}),
	}))
	resp := postSearch(t, h, testToken, map[string]any{"query": "go"})
	if resp.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", resp.Code, resp.Body.String())
	}
	out := decode(t, resp)
	if out["provider"] != "xai" {
		t.Fatalf("provider=%v", out["provider"])
	}
	if got := rec.snapshot(); len(got) != 2 || got[0] != "openai" || got[1] != "xai" {
		t.Fatalf("order=%v", got)
	}
}

func TestExplicitLLMSameOrder(t *testing.T) {
	rec := &callRecorder{}
	h := server.New(sources(map[string]search.Searcher{
		"openai": rec.track("openai", search.Fixed{Err: search.NewError(search.CodeBackendError, "openai down")}),
		"xai":    rec.track("xai", search.Fixed{Result: okResult("from xai")}),
	}))
	resp := postSearch(t, h, testToken, map[string]any{"query": "go", "provider": "llm"})
	if resp.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", resp.Code, resp.Body.String())
	}
	if decode(t, resp)["provider"] != "xai" {
		t.Fatalf("body=%s", resp.Body.String())
	}
	if got := rec.snapshot(); len(got) != 2 || got[0] != "openai" || got[1] != "xai" {
		t.Fatalf("order=%v", got)
	}
}

func TestAutoFallsBackAfterSearXNGEmpty(t *testing.T) {
	rec := &callRecorder{}
	h := server.New(sources(map[string]search.Searcher{
		"searxng": rec.track("searxng", search.Fixed{Result: search.Result{}}),
		"openai":  rec.track("openai", search.Fixed{Result: okResult("from openai")}),
		"xai":     rec.track("xai", search.Fixed{Result: okResult("from xai")}),
	}))
	resp := postSearch(t, h, testToken, map[string]any{"query": "go", "provider": "auto"})
	if resp.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", resp.Code, resp.Body.String())
	}
	if decode(t, resp)["provider"] != "openai" {
		t.Fatalf("body=%s", resp.Body.String())
	}
	if got := rec.snapshot(); len(got) != 2 || got[0] != "searxng" || got[1] != "openai" {
		t.Fatalf("order=%v", got)
	}
}

func TestAutoSkipsMissingOpenAI(t *testing.T) {
	rec := &callRecorder{}
	h := server.New(sources(map[string]search.Searcher{
		"searxng": rec.track("searxng", search.Fixed{Result: search.Result{}}),
		"xai":     rec.track("xai", search.Fixed{Result: okResult("from xai")}),
	}))
	resp := postSearch(t, h, testToken, map[string]any{"query": "go", "provider": "auto"})
	if resp.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", resp.Code, resp.Body.String())
	}
	if decode(t, resp)["provider"] != "xai" {
		t.Fatalf("body=%s", resp.Body.String())
	}
	if got := rec.snapshot(); len(got) != 2 || got[0] != "searxng" || got[1] != "xai" {
		t.Fatalf("order=%v", got)
	}
}

func TestLLMUsesOnlyConfiguredXAI(t *testing.T) {
	rec := &callRecorder{}
	h := server.New(sources(map[string]search.Searcher{
		"xai": rec.track("xai", search.Fixed{Result: okResult("from xai")}),
	}))
	resp := postSearch(t, h, testToken, map[string]any{"query": "go", "provider": "llm"})
	if resp.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", resp.Code, resp.Body.String())
	}
	if decode(t, resp)["provider"] != "xai" {
		t.Fatalf("body=%s", resp.Body.String())
	}
	if got := rec.snapshot(); len(got) != 1 || got[0] != "xai" {
		t.Fatalf("order=%v", got)
	}
}

func TestLLMNothingConfiguredMissingConfig(t *testing.T) {
	h := server.New(server.Config{Token: testToken, Sources: map[string]search.Searcher{}})
	resp := postSearch(t, h, testToken, map[string]any{"query": "go", "provider": "llm"})
	out := decode(t, resp)
	if resp.Code != http.StatusBadRequest || out["code"] != "missing_config" {
		t.Fatalf("status=%d body=%s", resp.Code, resp.Body.String())
	}
}

func TestNamedSearXNGDoesNotFallBack(t *testing.T) {
	rec := &callRecorder{}
	h := server.New(sources(map[string]search.Searcher{
		"searxng": rec.track("searxng", search.Fixed{Err: search.NewError(search.CodeBackendError, "searxng down")}),
		"openai":  rec.track("openai", search.Fixed{Result: okResult("from openai")}),
	}))
	resp := postSearch(t, h, testToken, map[string]any{"query": "go", "provider": "searxng"})
	out := decode(t, resp)
	if out["code"] != "backend_error" {
		t.Fatalf("body=%s", resp.Body.String())
	}
	if got := rec.snapshot(); len(got) != 1 || got[0] != "searxng" {
		t.Fatalf("order=%v", got)
	}
}

func TestNamedOpenAIUnconfiguredDoesNotCallXAI(t *testing.T) {
	rec := &callRecorder{}
	h := server.New(sources(map[string]search.Searcher{
		"xai": rec.track("xai", search.Fixed{Result: okResult("from xai")}),
	}))
	resp := postSearch(t, h, testToken, map[string]any{"query": "go", "provider": "openai"})
	out := decode(t, resp)
	if out["code"] != "missing_config" {
		t.Fatalf("body=%s", resp.Body.String())
	}
	if got := rec.snapshot(); len(got) != 0 {
		t.Fatalf("order=%v", got)
	}
}

func TestLLMStopsOnSourceAuthFailed(t *testing.T) {
	rec := &callRecorder{}
	h := server.New(sources(map[string]search.Searcher{
		"openai": rec.track("openai", search.Fixed{Err: search.NewError(search.CodeAuthFailed, "bad key")}),
		"xai":    rec.track("xai", search.Fixed{Result: okResult("from xai")}),
	}))
	resp := postSearch(t, h, testToken, map[string]any{"query": "go", "provider": "llm"})
	out := decode(t, resp)
	if resp.Code != http.StatusBadGateway || out["code"] != "auth_failed" {
		t.Fatalf("status=%d body=%s", resp.Code, resp.Body.String())
	}
	if got := rec.snapshot(); len(got) != 1 || got[0] != "openai" {
		t.Fatalf("order=%v", got)
	}
}

func TestLLMStopsOnStopCodes(t *testing.T) {
	cases := []struct {
		code   search.Code
		status int
	}{
		{search.CodeInvalidInput, http.StatusBadRequest},
		{search.CodeMissingConfig, http.StatusBadRequest},
		{search.CodeAborted, http.StatusInternalServerError},
		{search.CodeUnsupportedModel, http.StatusBadRequest},
		{search.CodeUnsupportedTool, http.StatusBadRequest},
		{search.CodeUnsupportedToolChoice, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(string(tc.code), func(t *testing.T) {
			rec := &callRecorder{}
			h := server.New(sources(map[string]search.Searcher{
				"openai": rec.track("openai", search.Fixed{Err: search.NewError(tc.code, "stop")}),
				"xai":    rec.track("xai", search.Fixed{Result: okResult("from xai")}),
			}))
			resp := postSearch(t, h, testToken, map[string]any{"query": "go", "provider": "llm"})
			out := decode(t, resp)
			if resp.Code != tc.status || out["code"] != string(tc.code) {
				t.Fatalf("status=%d body=%s", resp.Code, resp.Body.String())
			}
			if got := rec.snapshot(); len(got) != 1 {
				t.Fatalf("order=%v", got)
			}
		})
	}
}

func TestLLMContinuesOnContinueCodes(t *testing.T) {
	cases := []struct {
		name string
		err  error
		res  search.Result
	}{
		{"rate_limited", search.NewError(search.CodeRateLimited, "slow"), search.Result{}},
		{"timeout", search.NewError(search.CodeTimeout, "late"), search.Result{}},
		{"invalid_response", nil, search.Result{}},
		{"backend_error", search.NewError(search.CodeBackendError, "down"), search.Result{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := &callRecorder{}
			openai := search.Fixed{Result: tc.res, Err: tc.err}
			h := server.New(sources(map[string]search.Searcher{
				"openai": rec.track("openai", openai),
				"xai":    rec.track("xai", search.Fixed{Result: okResult("from xai")}),
			}))
			resp := postSearch(t, h, testToken, map[string]any{"query": "go", "provider": "llm"})
			if resp.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", resp.Code, resp.Body.String())
			}
			if decode(t, resp)["provider"] != "xai" {
				t.Fatalf("body=%s", resp.Body.String())
			}
			if got := rec.snapshot(); len(got) != 2 || got[0] != "openai" || got[1] != "xai" {
				t.Fatalf("order=%v", got)
			}
		})
	}
}

func TestProviderGoogleAndAllInvalid(t *testing.T) {
	h := server.New(sources(map[string]search.Searcher{
		"openai": search.Fixed{Result: okResult("ok")},
	}))
	for _, provider := range []any{"google", "all", "bing"} {
		resp := postSearch(t, h, testToken, map[string]any{"query": "go", "provider": provider})
		out := decode(t, resp)
		if resp.Code != http.StatusBadRequest || out["code"] != "invalid_input" {
			t.Fatalf("provider=%v status=%d body=%s", provider, resp.Code, resp.Body.String())
		}
	}
}

func TestProviderArrayInvalid(t *testing.T) {
	h := server.New(sources(map[string]search.Searcher{
		"openai": search.Fixed{Result: okResult("ok")},
		"xai":    search.Fixed{Result: okResult("ok")},
	}))
	resp := postSearch(t, h, testToken, map[string]any{"query": "go", "provider": []any{"openai", "xai"}})
	out := decode(t, resp)
	if resp.Code != http.StatusBadRequest || out["code"] != "invalid_input" {
		t.Fatalf("status=%d body=%s", resp.Code, resp.Body.String())
	}
}

func TestExhaustedChainReturnsLastError(t *testing.T) {
	h := server.New(sources(map[string]search.Searcher{
		"openai": search.Fixed{Err: search.NewError(search.CodeRateLimited, "openai")},
		"xai":    search.Fixed{Err: search.NewError(search.CodeTimeout, "xai")},
	}))
	resp := postSearch(t, h, testToken, map[string]any{"query": "go", "provider": "llm"})
	out := decode(t, resp)
	if resp.Code != http.StatusGatewayTimeout || out["code"] != "timeout" {
		t.Fatalf("status=%d body=%s", resp.Code, resp.Body.String())
	}
}
