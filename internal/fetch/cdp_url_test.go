package fetch_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sidkang/webgate/internal/fetch"
)

func TestResolveCDPWebsocketURLKeepsManagerPath(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewEncoder(w).Encode(map[string]any{
			"webSocketDebuggerUrl": "ws://127.0.0.1:9222/devtools/browser/abc",
		})
	}))
	t.Cleanup(srv.Close)

	endpoint := srv.URL + "/api/profiles/abc/cdp"
	wsURL, err := fetch.ResolveCDPWebsocketURLForTest(context.Background(), endpoint, "")
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/api/profiles/abc/cdp" {
		t.Fatalf("path=%q want /api/profiles/abc/cdp", gotPath)
	}
	if wsURL != "ws://127.0.0.1:9222/devtools/browser/abc" {
		t.Fatalf("wsURL=%q", wsURL)
	}
}

func TestResolveCDPWebsocketURLPassthroughWS(t *testing.T) {
	in := "ws://127.0.0.1:9222/devtools/browser/xyz"
	out, err := fetch.ResolveCDPWebsocketURLForTest(context.Background(), in, "")
	if err != nil {
		t.Fatal(err)
	}
	if out != in {
		t.Fatalf("out=%q", out)
	}
}
