package server_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sidkang/webgate/internal/server"
)

func getPath(h http.Handler, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestDebugPageEnabled(t *testing.T) {
	h := server.New(server.Config{Token: testToken, DebugUI: true})

	rec := getPath(h, "/debug")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Fatalf("Content-Type = %q, want text/html", ct)
	}
	body := rec.Body.String()
	if !strings.Contains(strings.ToLower(body), "<!doctype html") {
		t.Fatalf("body does not look like HTML: %.80s", body)
	}
	if strings.Contains(body, testToken) {
		t.Fatal("debug page must not contain the server token")
	}
	if !strings.Contains(body, `name="kernel"`) || !strings.Contains(body, `<option value="">omit</option>`) {
		t.Fatal("fetch kernel must be omittable")
	}
	if !strings.Contains(body, "Bearer token is required") {
		t.Fatal("empty token must be refused before send")
	}
}

func TestDebugPageRootRedirect(t *testing.T) {
	h := server.New(server.Config{Token: testToken, DebugUI: true})

	rec := getPath(h, "/")
	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/debug" {
		t.Fatalf("Location = %q, want /debug", loc)
	}
}

func TestDebugPageDisabled(t *testing.T) {
	h := server.New(server.Config{Token: testToken}) // DebugUI off (default)

	if rec := getPath(h, "/debug"); rec.Code != http.StatusNotFound {
		t.Fatalf("GET /debug status = %d, want 404", rec.Code)
	}
	if rec := getPath(h, "/"); rec.Code != http.StatusNotFound {
		t.Fatalf("GET / status = %d, want 404", rec.Code)
	}
}
