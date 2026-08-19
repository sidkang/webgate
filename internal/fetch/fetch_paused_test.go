package fetch_test

import (
	"testing"

	"github.com/sidkang/webgate/internal/fetch"
)

func TestDecideFetchPaused(t *testing.T) {
	const main = "main-frame"
	cases := []struct {
		name         string
		resourceType string
		frameID      string
		mainFrameID  string
		url          string
		want         fetch.PausedAction
	}{
		{"subresource script", "Script", main, main, "https://example.com/app.js", fetch.PausedContinue},
		{"subresource xhr", "XHR", main, main, "https://example.com/api", fetch.PausedContinue},
		{"iframe document", "Document", "iframe-1", main, "http://169.254.169.254/", fetch.PausedContinue},
		{"main metadata ip", "Document", main, main, "http://169.254.169.254/", fetch.PausedFail},
		{"main localhost", "Document", main, main, "http://127.0.0.1/", fetch.PausedFail},
		{"main example allow", "Document", main, main, "https://example.com/", fetch.PausedContinue},
		{"main 198.18 not special", "Document", main, main, "http://198.18.0.1/", fetch.PausedContinue},
		{"empty type main blocked", "", main, main, "http://169.254.169.254/", fetch.PausedFail},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := fetch.DecideFetchPaused(tc.resourceType, tc.frameID, tc.mainFrameID, tc.url)
			if got != tc.want {
				t.Fatalf("got %s want %s", got, tc.want)
			}
		})
	}
}
