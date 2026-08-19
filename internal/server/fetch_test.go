package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/sidkang/webgate/internal/fetch"
	"github.com/sidkang/webgate/internal/search"
	"github.com/sidkang/webgate/internal/server"
)

func postFetch(t *testing.T, h http.Handler, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/fetch", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func samplePage(url string) fetch.Page {
	return fetch.Page{
		RequestedURL: url,
		URL:          url,
		Title:        "Example",
		HTML:         `<html><body><article><h1>Example</h1><p>Hello readable page content.</p></article></body></html>`,
	}
}

func TestFetchRequiresBearer(t *testing.T) {
	h := server.New(server.Config{
		Token:    testToken,
		Capturer: fetch.FixedCapturer{Page: samplePage("https://example.com")},
	})
	rec := postFetch(t, h, "", map[string]any{"url": "https://example.com"})
	if rec.Code != http.StatusUnauthorized || decode(t, rec)["code"] != "auth_failed" {
		t.Fatalf("body=%s", rec.Body.String())
	}
}

func TestFetchRejectsBlockedURLs(t *testing.T) {
	var called int
	h := server.New(server.Config{
		Token: testToken,
		Capturer: fetch.CapturerFunc(func(ctx context.Context, u string) (fetch.Page, error) {
			called++
			return samplePage(u), nil
		}),
	})
	blocked := []string{
		"file:///tmp/x",
		"http://localhost/",
		"http://127.0.0.1/",
		"http://169.254.169.254/",
		"http://metadata/",
		"http://[::1]/",
	}
	for _, u := range blocked {
		rec := postFetch(t, h, testToken, map[string]any{"url": u})
		out := decode(t, rec)
		if rec.Code != http.StatusBadRequest || out["code"] != "invalid_input" {
			t.Fatalf("url=%s status=%d body=%s", u, rec.Code, rec.Body.String())
		}
	}
	if called != 0 {
		t.Fatalf("capturer called %d times", called)
	}
}

func TestFetchAllows19818ToReachCapturer(t *testing.T) {
	var got string
	h := server.New(server.Config{
		Token: testToken,
		Capturer: fetch.CapturerFunc(func(_ context.Context, u string) (fetch.Page, error) {
			got = u
			return samplePage(u), nil
		}),
		Kernels: fetch.KernelRunners{
			Defuddle: func(_ context.Context, _, _ string) (string, string, error) {
				return "T", "content from kernel", nil
			},
		},
	})
	rec := postFetch(t, h, testToken, map[string]any{"url": "http://198.18.0.1/"})
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got != "http://198.18.0.1/" {
		t.Fatalf("capturer url=%q", got)
	}
}

func TestFetchCloakDisabledDoesNotBreakSearch(t *testing.T) {
	h := server.New(server.Config{
		Token:         testToken,
		CloakDisabled: true,
		Capturer:      fetch.FixedCapturer{Page: samplePage("https://example.com")},
		Sources: map[string]search.Searcher{
			"searxng": search.Fixed{Result: search.Result{
				Answer:  "ok",
				Sources: []search.Source{{Title: "A", URL: "https://a.example", Snippet: "a"}},
			}},
		},
	})
	rec := postFetch(t, h, testToken, map[string]any{"url": "https://example.com"})
	if decode(t, rec)["code"] != "cloak_disabled" || rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("fetch body=%s", rec.Body.String())
	}
	srec := postSearch(t, h, testToken, map[string]any{"query": "go", "provider": "searxng"})
	if srec.Code != http.StatusOK {
		t.Fatalf("search status=%d body=%s", srec.Code, srec.Body.String())
	}
}

func TestFetchMissingEndpoint(t *testing.T) {
	h := server.New(server.Config{Token: testToken})
	rec := postFetch(t, h, testToken, map[string]any{"url": "https://example.com"})
	out := decode(t, rec)
	if rec.Code != http.StatusBadRequest || out["code"] != "missing_config" {
		t.Fatalf("body=%s", rec.Body.String())
	}
}

func TestFetchRejectsProfile(t *testing.T) {
	h := server.New(server.Config{
		Token:    testToken,
		Capturer: fetch.FixedCapturer{Page: samplePage("https://example.com")},
	})
	rec := postFetch(t, h, testToken, map[string]any{"url": "https://example.com", "profile": "abc"})
	if decode(t, rec)["code"] != "invalid_input" {
		t.Fatalf("body=%s", rec.Body.String())
	}
}

func TestFetchModesShareCapturer(t *testing.T) {
	var mu sync.Mutex
	var urls []string
	h := server.New(server.Config{
		Token: testToken,
		Capturer: fetch.CapturerFunc(func(_ context.Context, u string) (fetch.Page, error) {
			mu.Lock()
			urls = append(urls, u)
			mu.Unlock()
			return samplePage(u), nil
		}),
		Kernels: fetch.KernelRunners{
			Defuddle: func(_ context.Context, _, _ string) (string, string, error) {
				return "Title", "readable body", nil
			},
		},
	})
	target := "https://example.com/page"
	r1 := postFetch(t, h, testToken, map[string]any{"url": target, "mode": "raw"})
	r2 := postFetch(t, h, testToken, map[string]any{"url": target, "mode": "readable"})
	if r1.Code != http.StatusOK || r2.Code != http.StatusOK {
		t.Fatalf("raw=%s readable=%s", r1.Body.String(), r2.Body.String())
	}
	outRaw := decode(t, r1)
	outRead := decode(t, r2)
	if outRaw["mode"] != "raw" || outRaw["kernel"] != "" {
		t.Fatalf("raw=%v", outRaw)
	}
	if outRead["mode"] != "readable" || outRead["kernel"] != "defuddle" {
		t.Fatalf("readable=%v", outRead)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(urls) != 2 || urls[0] != target || urls[1] != target {
		t.Fatalf("urls=%v", urls)
	}
}

func TestFetchKernelFallbackChain(t *testing.T) {
	var called []string
	h := server.New(server.Config{
		Token:    testToken,
		Capturer: fetch.FixedCapturer{Page: samplePage("https://example.com")},
		Kernels: fetch.KernelRunners{
			Defuddle: func(_ context.Context, _, _ string) (string, string, error) {
				called = append(called, "defuddle")
				return "", "", nil // empty → continue
			},
			HTMLExtractor: func(_ context.Context, _, _ string) (string, string, error) {
				called = append(called, "html-extractor")
				return "H", "from html-extractor", nil
			},
			LLM: func(_ context.Context, _, _, _ string) (string, string, error) {
				called = append(called, "llm")
				return "L", "from llm", nil
			},
		},
	})
	rec := postFetch(t, h, testToken, map[string]any{"url": "https://example.com"})
	out := decode(t, rec)
	if rec.Code != http.StatusOK || out["kernel"] != "html-extractor" {
		t.Fatalf("body=%s", rec.Body.String())
	}
	if len(called) != 2 || called[0] != "defuddle" || called[1] != "html-extractor" {
		t.Fatalf("called=%v", called)
	}
}

func TestFetchKernelHTMLExtractorSkipsDefuddle(t *testing.T) {
	var called []string
	h := server.New(server.Config{
		Token:    testToken,
		Capturer: fetch.FixedCapturer{Page: samplePage("https://example.com")},
		Kernels: fetch.KernelRunners{
			Defuddle: func(_ context.Context, _, _ string) (string, string, error) {
				called = append(called, "defuddle")
				return "D", "defuddle", nil
			},
			HTMLExtractor: func(_ context.Context, _, _ string) (string, string, error) {
				called = append(called, "html-extractor")
				return "H", "html", nil
			},
		},
	})
	rec := postFetch(t, h, testToken, map[string]any{"url": "https://example.com", "kernel": "html-extractor"})
	if decode(t, rec)["kernel"] != "html-extractor" {
		t.Fatalf("body=%s", rec.Body.String())
	}
	if len(called) != 1 || called[0] != "html-extractor" {
		t.Fatalf("called=%v", called)
	}
}

func TestFetchLLMMissingConfig(t *testing.T) {
	h := server.New(server.Config{
		Token:    testToken,
		Capturer: fetch.FixedCapturer{Page: samplePage("https://example.com")},
		Kernels:  fetch.KernelRunners{}, // no LLM
	})
	rec := postFetch(t, h, testToken, map[string]any{"url": "https://example.com", "kernel": "llm"})
	if decode(t, rec)["code"] != "missing_config" {
		t.Fatalf("body=%s", rec.Body.String())
	}
}

func TestFetchLocalChainSkipsMissingLLM(t *testing.T) {
	h := server.New(server.Config{
		Token:    testToken,
		Capturer: fetch.FixedCapturer{Page: samplePage("https://example.com")},
		Kernels: fetch.KernelRunners{
			Defuddle: func(_ context.Context, _, _ string) (string, string, error) {
				return "", "", nil
			},
			HTMLExtractor: func(_ context.Context, _, _ string) (string, string, error) {
				return "H", "ok html", nil
			},
			// LLM nil → skipped
		},
	})
	rec := postFetch(t, h, testToken, map[string]any{"url": "https://example.com"})
	if decode(t, rec)["kernel"] != "html-extractor" {
		t.Fatalf("body=%s", rec.Body.String())
	}
}

func TestFetchKernelAbortStopsChain(t *testing.T) {
	var called []string
	h := server.New(server.Config{
		Token:    testToken,
		Capturer: fetch.FixedCapturer{Page: samplePage("https://example.com")},
		Kernels: fetch.KernelRunners{
			Defuddle: func(_ context.Context, _, _ string) (string, string, error) {
				called = append(called, "defuddle")
				return "", "", fetch.NewError(fetch.CodeAborted, "aborted")
			},
			HTMLExtractor: func(_ context.Context, _, _ string) (string, string, error) {
				called = append(called, "html-extractor")
				return "H", "ok", nil
			},
		},
	})
	rec := postFetch(t, h, testToken, map[string]any{"url": "https://example.com"})
	out := decode(t, rec)
	if out["code"] != "aborted" {
		t.Fatalf("body=%s", rec.Body.String())
	}
	if len(called) != 1 {
		t.Fatalf("called=%v", called)
	}
}

func TestFetchKernelTimeoutStopsChain(t *testing.T) {
	var called []string
	h := server.New(server.Config{
		Token:    testToken,
		Capturer: fetch.FixedCapturer{Page: samplePage("https://example.com")},
		Kernels: fetch.KernelRunners{
			Defuddle: func(_ context.Context, _, _ string) (string, string, error) {
				called = append(called, "defuddle")
				return "", "", fetch.NewError(fetch.CodeTimeout, "timeout")
			},
			HTMLExtractor: func(_ context.Context, _, _ string) (string, string, error) {
				called = append(called, "html-extractor")
				return "H", "ok", nil
			},
		},
	})
	rec := postFetch(t, h, testToken, map[string]any{"url": "https://example.com"})
	if decode(t, rec)["code"] != "timeout" || rec.Code != http.StatusGatewayTimeout {
		t.Fatalf("body=%s", rec.Body.String())
	}
	if len(called) != 1 {
		t.Fatalf("called=%v", called)
	}
}

func TestFetchTruncationNotice(t *testing.T) {
	long := strings.Repeat("x", 40_000)
	h := server.New(server.Config{
		Token:    testToken,
		Capturer: fetch.FixedCapturer{Page: samplePage("https://example.com")},
		Kernels: fetch.KernelRunners{
			Defuddle: func(_ context.Context, _, _ string) (string, string, error) {
				return "T", long, nil
			},
		},
	})
	rec := postFetch(t, h, testToken, map[string]any{"url": "https://example.com"})
	out := decode(t, rec)
	if out["truncated"] != true {
		t.Fatalf("body=%s", rec.Body.String())
	}
	content, _ := out["content"].(string)
	if !strings.Contains(content, "omitted tail is not retrievable") {
		t.Fatalf("missing notice: %s", content[len(content)-120:])
	}
	if _, ok := out["tail"]; ok {
		t.Fatal("tail field must not exist")
	}
	if int(out["returnedChars"].(float64)) > fetch.DefaultMaxInlineChars {
		t.Fatalf("returnedChars=%v", out["returnedChars"])
	}
}

func TestFetchInvalidModeKernel(t *testing.T) {
	h := server.New(server.Config{
		Token:    testToken,
		Capturer: fetch.FixedCapturer{Page: samplePage("https://example.com")},
	})
	for _, body := range []map[string]any{
		{"url": "https://example.com", "mode": "other"},
		{"url": "https://example.com", "kernel": "other"},
	} {
		rec := postFetch(t, h, testToken, body)
		if decode(t, rec)["code"] != "invalid_input" {
			t.Fatalf("body=%s", rec.Body.String())
		}
	}
}
