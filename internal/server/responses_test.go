package server_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/sidkang/webgate/internal/search"
	"github.com/sidkang/webgate/internal/server"
)

func fakeResponses(t *testing.T, handler http.HandlerFunc) (*httptest.Server, *search.ResponsesClient) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// openai-go requires application/json Content-Type on success/error bodies.
		if r.URL.Path != "/responses" && r.URL.Path != "/v1/responses" {
			http.NotFound(w, r)
			return
		}
		handler(&jsonContentType{ResponseWriter: w}, r)
	}))
	t.Cleanup(srv.Close)
	return srv, &search.ResponsesClient{BaseURL: srv.URL, APIKey: "test-key", HTTPClient: srv.Client()}
}

// jsonContentType forces application/json so openai-go can decode fake Responses replies.
type jsonContentType struct {
	http.ResponseWriter
	wrote bool
}

func (j *jsonContentType) WriteHeader(code int) {
	if !j.wrote {
		j.Header().Set("Content-Type", "application/json")
		j.wrote = true
	}
	j.ResponseWriter.WriteHeader(code)
}

func (j *jsonContentType) Write(b []byte) (int, error) {
	if !j.wrote {
		j.WriteHeader(http.StatusOK)
	}
	return j.ResponseWriter.Write(b)
}

func successWebBody(answer, url string) map[string]any {
	return map[string]any{
		"output_text": answer,
		"output": []any{
			map[string]any{
				"type": "message",
				"content": []any{
					map[string]any{
						"type": "output_text",
						"text": answer,
						"annotations": []any{
							map[string]any{"type": "url_citation", "url": url, "title": "T"},
						},
					},
				},
			},
		},
	}
}

func TestLLMFallsBackOpenAIToXAI(t *testing.T) {
	var hits []string
	var mu sync.Mutex
	srv, client := fakeResponses(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		defer mu.Unlock()
		if strings.Contains(string(body), `"model":"openai-model"`) {
			hits = append(hits, "openai")
			http.Error(w, `{"error":{"message":"boom"}}`, http.StatusBadGateway)
			return
		}
		hits = append(hits, "xai")
		_ = json.NewEncoder(w).Encode(successWebBody("from xai", "https://example.com/x"))
	})
	_ = srv
	h := server.New(server.Config{
		Token: testToken,
		Sources: map[string]search.Searcher{
			"openai": search.OpenAI{Client: &search.ResponsesClient{BaseURL: client.BaseURL, APIKey: "k", HTTPClient: client.HTTPClient}, Model: "openai-model"},
			"xai":    search.XAI{Client: &search.ResponsesClient{BaseURL: client.BaseURL, APIKey: "k", HTTPClient: client.HTTPClient}, Model: "xai-model"},
		},
	})
	rec := postSearch(t, h, testToken, map[string]any{"query": "go"})
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if decode(t, rec)["provider"] != "xai" {
		t.Fatalf("body=%s", rec.Body.String())
	}
	mu.Lock()
	defer mu.Unlock()
	if len(hits) < 2 || hits[0] != "openai" || hits[1] != "xai" {
		t.Fatalf("hits=%v", hits)
	}
}

func TestAutoUsesOpenAIWhenSearXNGMissing(t *testing.T) {
	_, client := fakeResponses(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(successWebBody("from openai", "https://example.com/o"))
	})
	h := server.New(server.Config{
		Token: testToken,
		Sources: map[string]search.Searcher{
			"openai": search.OpenAI{Client: client, Model: "gpt"},
		},
	})
	rec := postSearch(t, h, testToken, map[string]any{"query": "go", "provider": "auto"})
	if rec.Code != http.StatusOK || decode(t, rec)["provider"] != "openai" {
		t.Fatalf("body=%s", rec.Body.String())
	}
}

func TestNamedOpenAIRequestShape(t *testing.T) {
	var got map[string]any
	_, client := fakeResponses(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		_ = json.NewEncoder(w).Encode(successWebBody("ok", "https://example.com"))
	})
	h := server.New(server.Config{
		Token: testToken,
		Sources: map[string]search.Searcher{
			"openai": search.OpenAI{Client: client, Model: "gpt-test"},
		},
	})
	rec := postSearch(t, h, testToken, map[string]any{
		"query":    "hello",
		"provider": "openai",
		"user_location": map[string]any{
			"country": "China",
			"city":    "Shanghai",
		},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("body=%s", rec.Body.String())
	}
	tools := got["tools"].([]any)
	tool := tools[0].(map[string]any)
	if tool["external_web_access"] != true {
		t.Fatalf("tool=%v", tool)
	}
	if tool["search_context_size"] != "high" {
		t.Fatalf("context=%v", tool["search_context_size"])
	}
	tc := got["tool_choice"].(map[string]any)
	if tc["type"] != "web_search" {
		t.Fatalf("tool_choice=%v", tc)
	}
	loc, _ := tool["user_location"].(map[string]any)
	if loc == nil {
		t.Fatal("expected user_location with city")
	}
	if _, ok := loc["country"]; ok {
		t.Fatalf("China should be dropped: %v", loc)
	}
	if loc["city"] != "Shanghai" {
		t.Fatalf("loc=%v", loc)
	}
}

func TestNamedOpenAISendsCNCountry(t *testing.T) {
	var got map[string]any
	_, client := fakeResponses(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		_ = json.NewEncoder(w).Encode(successWebBody("ok", "https://example.com"))
	})
	h := server.New(server.Config{
		Token: testToken,
		Sources: map[string]search.Searcher{
			"openai": search.OpenAI{Client: client, Model: "gpt"},
		},
	})
	rec := postSearch(t, h, testToken, map[string]any{
		"query":    "hello",
		"provider": "openai",
		"user_location": map[string]any{
			"country": "CN",
		},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("body=%s", rec.Body.String())
	}
	tool := got["tools"].([]any)[0].(map[string]any)
	loc := tool["user_location"].(map[string]any)
	if loc["country"] != "CN" {
		t.Fatalf("loc=%v", loc)
	}
}

func TestXAIRejectsMoreThanFiveDomains(t *testing.T) {
	called := false
	_, client := fakeResponses(t, func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})
	h := server.New(server.Config{
		Token: testToken,
		Sources: map[string]search.Searcher{
			"xai": search.XAI{Client: client, Model: "grok"},
		},
	})
	domains := []any{"a.com", "b.com", "c.com", "d.com", "e.com", "f.com"}
	rec := postSearch(t, h, testToken, map[string]any{
		"query":           "q",
		"provider":        "xai",
		"allowed_domains": domains,
	})
	if decode(t, rec)["code"] != "invalid_input" {
		t.Fatalf("body=%s", rec.Body.String())
	}
	if called {
		t.Fatal("upstream should not be called")
	}
}

func TestXAIRetriesWithoutToolChoice(t *testing.T) {
	var bodies []map[string]any
	_, client := fakeResponses(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		bodies = append(bodies, body)
		if _, ok := body["tool_choice"]; ok {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"unsupported tool_choice / modeltoolchoice"}}`))
			return
		}
		_ = json.NewEncoder(w).Encode(successWebBody("retried", "https://example.com"))
	})
	h := server.New(server.Config{
		Token: testToken,
		Sources: map[string]search.Searcher{
			"xai": search.XAI{Client: client, Model: "grok"},
		},
	})
	rec := postSearch(t, h, testToken, map[string]any{"query": "q", "provider": "xai"})
	if rec.Code != http.StatusOK {
		t.Fatalf("body=%s", rec.Body.String())
	}
	if len(bodies) != 2 {
		t.Fatalf("bodies=%d", len(bodies))
	}
	if _, ok := bodies[0]["tool_choice"]; !ok {
		t.Fatal("first request needs tool_choice")
	}
	if _, ok := bodies[1]["tool_choice"]; ok {
		t.Fatal("retry must omit tool_choice")
	}
}

func TestNamedOpenAIAnswerOnlyInvalidResponse(t *testing.T) {
	_, client := fakeResponses(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"output_text": "answer with no structured citations",
			"output": []any{
				map[string]any{
					"type": "message",
					"content": []any{
						map[string]any{"type": "output_text", "text": "answer with no structured citations"},
					},
				},
			},
		})
	})
	h := server.New(server.Config{
		Token: testToken,
		Sources: map[string]search.Searcher{
			"openai": search.OpenAI{Client: client, Model: "gpt"},
		},
	})
	rec := postSearch(t, h, testToken, map[string]any{"query": "q", "provider": "openai"})
	if decode(t, rec)["code"] != "invalid_response" {
		t.Fatalf("body=%s", rec.Body.String())
	}
}

func TestNamedOpenAIEmptyShellInvalidResponse(t *testing.T) {
	_, client := fakeResponses(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"output": []any{}})
	})
	h := server.New(server.Config{
		Token: testToken,
		Sources: map[string]search.Searcher{
			"openai": search.OpenAI{Client: client, Model: "gpt"},
		},
	})
	rec := postSearch(t, h, testToken, map[string]any{"query": "q", "provider": "openai"})
	if decode(t, rec)["code"] != "invalid_response" {
		t.Fatalf("body=%s", rec.Body.String())
	}
}

func TestOfficialXAIBaseMissingConfig(t *testing.T) {
	h := server.New(server.Config{
		Token: testToken,
		Sources: map[string]search.Searcher{
			"xai": search.XAI{
				Client: &search.ResponsesClient{BaseURL: "https://api.x.ai/v1", APIKey: "k"},
				Model:  "grok",
			},
		},
	})
	rec := postSearch(t, h, testToken, map[string]any{"query": "q", "provider": "xai"})
	if decode(t, rec)["code"] != "missing_config" {
		t.Fatalf("body=%s", rec.Body.String())
	}
	h2 := server.New(server.Config{
		Token: testToken,
		Sources: map[string]search.Searcher{
			"xai": search.XAI{
				Client: &search.ResponsesClient{BaseURL: "https://proxy.api.x.ai/v1", APIKey: "k"},
				Model:  "grok",
			},
		},
	})
	rec2 := postSearch(t, h2, testToken, map[string]any{"query": "q", "provider": "xai"})
	if decode(t, rec2)["code"] != "missing_config" {
		t.Fatalf("subdomain body=%s", rec2.Body.String())
	}
}

func postXSearch(t *testing.T, h http.Handler, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/x_search", strings.NewReader(string(raw)))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestXSearchInvalidDates(t *testing.T) {
	_, client := fakeResponses(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("should not call upstream")
	})
	h := server.New(server.Config{
		Token:   testToken,
		XSearch: &search.XSearch{Client: client, Model: "grok"},
	})
	for _, body := range []map[string]any{
		{"query": "q", "from_date": "2024-13-01"},
		{"query": "q", "from_date": "2024-01-01", "to_date": "2023-01-01"},
		{"query": "q", "to_date": "not-a-date"},
	} {
		rec := postXSearch(t, h, testToken, body)
		if decode(t, rec)["code"] != "invalid_input" {
			t.Fatalf("body=%v resp=%s", body, rec.Body.String())
		}
	}
}

func TestXSearchRequiresXEvidence(t *testing.T) {
	_, client := fakeResponses(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"output_text": "generic chat with no X evidence",
			"output":      []any{map[string]any{"type": "message"}},
		})
	})
	h := server.New(server.Config{
		Token:   testToken,
		XSearch: &search.XSearch{Client: client, Model: "grok"},
	})
	rec := postXSearch(t, h, testToken, map[string]any{"query": "q"})
	if decode(t, rec)["code"] != "invalid_response" {
		t.Fatalf("body=%s", rec.Body.String())
	}
}

func TestXSearchScrapedXURLIsInvalidResponse(t *testing.T) {
	_, client := fakeResponses(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"output_text": "See https://x.com/user/status/1",
			"output":      []any{map[string]any{"type": "message"}},
		})
	})
	h := server.New(server.Config{
		Token:   testToken,
		XSearch: &search.XSearch{Client: client, Model: "grok"},
	})
	rec := postXSearch(t, h, testToken, map[string]any{"query": "q"})
	if decode(t, rec)["code"] != "invalid_response" {
		t.Fatalf("scraped-only body=%s", rec.Body.String())
	}
}

func TestXSearchStructuredCitationSucceeds(t *testing.T) {
	_, client := fakeResponses(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"output": []any{
				map[string]any{
					"type": "message",
					"content": []any{
						map[string]any{
							"type": "output_text",
							"text": "from a post",
							"annotations": []any{
								map[string]any{
									"type":  "url_citation",
									"url":   "https://x.com/user/status/1",
									"title": "post",
								},
							},
						},
					},
				},
			},
		})
	})
	h := server.New(server.Config{
		Token:   testToken,
		XSearch: &search.XSearch{Client: client, Model: "grok"},
	})
	rec := postXSearch(t, h, testToken, map[string]any{"query": "q"})
	if rec.Code != http.StatusOK {
		t.Fatalf("body=%s", rec.Body.String())
	}
	if decode(t, rec)["answer"] != "from a post" {
		t.Fatalf("body=%s", rec.Body.String())
	}
}

func TestXSearchCitationsArraySucceeds(t *testing.T) {
	_, client := fakeResponses(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"output_text": "from citations array",
			"citations": []any{
				map[string]any{"url": "https://twitter.com/user/status/2", "title": "t"},
			},
			"output": []any{},
		})
	})
	h := server.New(server.Config{
		Token:   testToken,
		XSearch: &search.XSearch{Client: client, Model: "grok"},
	})
	rec := postXSearch(t, h, testToken, map[string]any{"query": "q"})
	if rec.Code != http.StatusOK {
		t.Fatalf("body=%s", rec.Body.String())
	}
}

func TestXSearchSuccessWithCall(t *testing.T) {
	var got map[string]any
	_, client := fakeResponses(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"output_text": "from X posts",
			"output": []any{
				map[string]any{"type": "x_search_call"},
				map[string]any{
					"type": "message",
					"content": []any{
						map[string]any{"type": "output_text", "text": "from X posts"},
					},
				},
			},
		})
	})
	h := server.New(server.Config{
		Token:   testToken,
		XSearch: &search.XSearch{Client: client, Model: "grok"},
	})
	rec := postXSearch(t, h, testToken, map[string]any{"query": "q", "from_date": "2024-01-01"})
	if rec.Code != http.StatusOK {
		t.Fatalf("body=%s", rec.Body.String())
	}
	tools := got["tools"].([]any)
	tool := tools[0].(map[string]any)
	if tool["type"] != "x_search" {
		t.Fatalf("tool=%v", tool)
	}
	if _, ok := tool["type"].(string); ok {
		if tool["type"] == "web_search" {
			t.Fatal("must not use web_search")
		}
	}
	if got["instructions"] != search.XSearchInstructions {
		t.Fatalf("instructions=%v", got["instructions"])
	}
}

func TestXSearchOfficialHostMissingConfig(t *testing.T) {
	h := server.New(server.Config{
		Token: testToken,
		XSearch: &search.XSearch{
			Client: &search.ResponsesClient{BaseURL: "https://api.x.ai/v1/responses", APIKey: "k"},
			Model:  "grok",
		},
	})
	rec := postXSearch(t, h, testToken, map[string]any{"query": "q"})
	if decode(t, rec)["code"] != "missing_config" {
		t.Fatalf("body=%s", rec.Body.String())
	}
}

func TestSearchContextSizeInvalid(t *testing.T) {
	h := server.New(server.Config{
		Token: testToken,
		Sources: map[string]search.Searcher{
			"openai": search.Fixed{Result: okResult("x")},
		},
	})
	rec := postSearch(t, h, testToken, map[string]any{
		"query": "q", "provider": "openai", "search_context_size": "huge",
	})
	if decode(t, rec)["code"] != "invalid_input" {
		t.Fatalf("body=%s", rec.Body.String())
	}
}
