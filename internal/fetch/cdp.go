package fetch

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
	"github.com/gobwas/ws"
)

// CDPCapturer attaches to a remote Chrome DevTools endpoint (no local Chrome launch).
type CDPCapturer struct {
	Endpoint string
	APIKey   string
	Lookup   Lookup // optional SSRF DNS lookup
}

var wsDialMu sync.Mutex

// Capture opens a background tab, navigates, waits/scrolls briefly, reads HTML, and closes only that tab.
func (c CDPCapturer) Capture(ctx context.Context, pageURL string) (Page, error) {
	if strings.TrimSpace(c.Endpoint) == "" {
		return Page{}, NewError(CodeMissingConfig, "No CDP endpoint is configured. Set CDP_ENDPOINT.")
	}
	if _, err := AssertFetchURLAllowed(ctx, pageURL, c.Lookup); err != nil {
		return Page{}, err
	}

	wsURL, err := resolveCDPWebsocketURL(ctx, c.Endpoint, c.APIKey)
	if err != nil {
		if ctx.Err() != nil {
			return Page{}, mapCtxErr(ctx.Err())
		}
		return Page{}, NewError(CodeBackendError, "cdp endpoint discovery failed")
	}

	// chromedp's websocket dial has no header hook; temporarily set DefaultDialer headers.
	if c.APIKey != "" {
		wsDialMu.Lock()
		prev := ws.DefaultDialer
		ws.DefaultDialer = ws.Dialer{
			Header: ws.HandshakeHeaderHTTP(http.Header{
				"Authorization": {"Bearer " + c.APIKey},
			}),
		}
		defer func() {
			ws.DefaultDialer = prev
			wsDialMu.Unlock()
		}()
	}

	allocCtx, allocCancel := chromedp.NewRemoteAllocator(ctx, wsURL, chromedp.NoModifyURL)
	defer allocCancel()

	tabCtx, tabCancel := chromedp.NewContext(allocCtx)
	defer tabCancel()

	var (
		finalURL string
		title    string
		html     string
	)

	runErr := chromedp.Run(tabCtx,
		chromedp.Navigate(pageURL),
		chromedp.ActionFunc(func(ctx context.Context) error {
			return boundedLazyLoad(ctx)
		}),
		chromedp.Location(&finalURL),
		chromedp.Title(&title),
		chromedp.OuterHTML("html", &html, chromedp.ByQuery),
	)
	if runErr != nil {
		if ctx.Err() != nil {
			return Page{}, mapCtxErr(ctx.Err())
		}
		if tabCtx.Err() != nil {
			return Page{}, mapCtxErr(tabCtx.Err())
		}
		return Page{}, NewError(CodeBackendError, "cdp capture failed")
	}

	if finalURL != "" && finalURL != pageURL {
		if _, gerr := AssertFetchURLAllowed(ctx, finalURL, c.Lookup); gerr != nil {
			return Page{}, gerr
		}
	}
	if len([]byte(html)) > MaxCaptureBytes {
		return Page{}, NewError(CodeBackendError, fmt.Sprintf("Captured HTML exceeds the %d MiB limit.", MaxCaptureBytes/(1024*1024)))
	}

	_ = chromedp.Run(tabCtx, chromedp.ActionFunc(func(ctx context.Context) error {
		t := chromedp.FromContext(ctx).Target
		if t == nil {
			return nil
		}
		return target.CloseTarget(t.TargetID).Do(ctx)
	}))

	if finalURL == "" {
		finalURL = pageURL
	}
	return Page{
		RequestedURL: pageURL,
		URL:          finalURL,
		Title:        title,
		HTML:         html,
	}, nil
}

func resolveCDPWebsocketURL(ctx context.Context, endpoint, apiKey string) (string, error) {
	if strings.Contains(endpoint, "/devtools/browser/") {
		return endpoint, nil
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", err
	}
	switch u.Scheme {
	case "ws":
		u.Scheme = "http"
	case "wss":
		u.Scheme = "https"
	case "http", "https":
		// ok
	default:
		return "", fmt.Errorf("unsupported cdp endpoint scheme")
	}
	u.Path = "/json/version"
	u.RawQuery = ""
	u.Fragment = ""

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", err
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("cdp version HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", err
	}
	wsURL, _ := payload["webSocketDebuggerUrl"].(string)
	if strings.TrimSpace(wsURL) == "" {
		return "", fmt.Errorf("cdp version missing webSocketDebuggerUrl")
	}
	return wsURL, nil
}

func boundedLazyLoad(ctx context.Context) error {
	deadline := time.Now().Add(LazyLoadMaxMS * time.Millisecond)
	scrolled := 0.0
	for step := 0; step < LazyLoadMaxSteps; step++ {
		if time.Now().After(deadline) {
			break
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if scrolled+LazyLoadStepViewports > LazyLoadMaxDistanceViewports+1e-9 {
			break
		}
		var ok bool
		script := fmt.Sprintf(`(() => {
			const h = window.innerHeight || 800;
			const before = window.scrollY || 0;
			window.scrollBy(0, h * %g);
			return (window.scrollY || 0) > before;
		})()`, LazyLoadStepViewports)
		if err := chromedp.Evaluate(script, &ok).Do(ctx); err != nil {
			return nil
		}
		scrolled += LazyLoadStepViewports
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
		if !ok {
			break
		}
	}
	return nil
}
