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

	"github.com/chromedp/cdproto/cdp"
	cdpfetch "github.com/chromedp/cdproto/fetch"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
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

// Capture opens a background tab, enables main-frame Document Fetch SSRF, navigates,
// waits/scrolls briefly, reads HTML, and closes only that tab.
// Never Fetch.disable (would resume a blocked redirect).
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

	// Bootstrap context allocates the remote browser and a temporary tab we close.
	bootCtx, bootCancel := chromedp.NewContext(allocCtx)
	defer bootCancel()

	var bgTargetID target.ID
	if err := chromedp.Run(bootCtx, chromedp.ActionFunc(func(ctx context.Context) error {
		c := chromedp.FromContext(ctx)
		browserExec := cdp.WithExecutor(ctx, c.Browser)
		id, err := target.CreateTarget("about:blank").WithBackground(true).Do(browserExec)
		if err != nil {
			return err
		}
		bgTargetID = id
		// Close the auto-created foreground tab; keep only the background target.
		if c.Target != nil && c.Target.TargetID != "" && c.Target.TargetID != bgTargetID {
			_ = target.CloseTarget(c.Target.TargetID).Do(browserExec)
		}
		return nil
	})); err != nil {
		if ctx.Err() != nil {
			return Page{}, mapCtxErr(ctx.Err())
		}
		return Page{}, NewError(CodeBackendError, "cdp create target failed")
	}

	tabCtx, tabCancel := chromedp.NewContext(bootCtx, chromedp.WithTargetID(bgTargetID))
	defer tabCancel()

	lookup := c.Lookup

	var (
		guardMu   sync.Mutex
		guardErr  error
		mainFrame cdp.FrameID
		pending   sync.WaitGroup
		openReqs  sync.Map // requestID → struct{}
	)

	setGuardErr := func(err error) {
		if err == nil {
			return
		}
		guardMu.Lock()
		if guardErr == nil {
			guardErr = err
		}
		guardMu.Unlock()
	}
	getGuardErr := func() error {
		guardMu.Lock()
		defer guardMu.Unlock()
		return guardErr
	}

	failPaused := func(requestID cdpfetch.RequestID) {
		// Independent of capture ctx — host TEARDOWN_TIMEOUT_MS.
		tctx, cancel := context.WithTimeout(context.Background(), time.Duration(TeardownTimeoutMS)*time.Millisecond)
		defer cancel()
		cc := chromedp.FromContext(tabCtx)
		if cc == nil || cc.Target == nil {
			return
		}
		exec := cdp.WithExecutor(tctx, cc.Target)
		_ = cdpfetch.FailRequest(requestID, network.ErrorReasonBlockedByClient).Do(exec)
	}

	chromedp.ListenTarget(tabCtx, func(ev any) {
		paused, ok := ev.(*cdpfetch.EventRequestPaused)
		if !ok || paused == nil {
			return
		}
		pending.Add(1)
		go func(ev *cdpfetch.EventRequestPaused) {
			defer pending.Done()
			reqURL := ""
			if ev.Request != nil {
				reqURL = ev.Request.URL
			}
			resourceType := string(ev.ResourceType)
			frameID := string(ev.FrameID)
			mainID := string(mainFrame)

			action := DecideFetchPaused(resourceType, frameID, mainID, reqURL)
			cc := chromedp.FromContext(tabCtx)
			if cc == nil || cc.Target == nil {
				return
			}

			if action == PausedFail {
				openReqs.Store(string(ev.RequestID), struct{}{})
				failPaused(ev.RequestID)
				openReqs.Delete(string(ev.RequestID))
				setGuardErr(NewError(CodeInvalidInput, "URL hostname is blocked for local, private, or metadata destinations."))
				return
			}

			if IsMainFrameDocument(resourceType, frameID, mainID) {
				openReqs.Store(string(ev.RequestID), struct{}{})
				defer openReqs.Delete(string(ev.RequestID))
				if _, err := AssertFetchURLAllowed(ctx, reqURL, lookup); err != nil {
					failPaused(ev.RequestID)
					setGuardErr(err)
					return
				}
			}

			contCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
			defer cancel()
			exec := cdp.WithExecutor(contCtx, cc.Target)
			if err := cdpfetch.ContinueRequest(ev.RequestID).Do(exec); err != nil {
				if ctx.Err() != nil {
					setGuardErr(mapCtxErr(ctx.Err()))
				}
			}
		}(paused)
	})

	setupErr := chromedp.Run(tabCtx, chromedp.ActionFunc(func(ctx context.Context) error {
		if err := page.Enable().Do(ctx); err != nil {
			return err
		}
		tree, err := page.GetFrameTree().Do(ctx)
		if err != nil {
			return err
		}
		if tree == nil || tree.Frame == nil {
			return fmt.Errorf("cdp missing main frame")
		}
		mainFrame = tree.Frame.ID
		return cdpfetch.Enable().WithPatterns([]*cdpfetch.RequestPattern{{
			RequestStage: cdpfetch.RequestStageRequest,
			ResourceType: network.ResourceTypeDocument,
		}}).Do(ctx)
	}))
	if setupErr != nil {
		if ctx.Err() != nil {
			return Page{}, mapCtxErr(ctx.Err())
		}
		return Page{}, NewError(CodeBackendError, "cdp fetch enable failed")
	}

	var (
		finalURL string
		title    string
		html     string
	)

	runErr := chromedp.Run(tabCtx,
		chromedp.Navigate(pageURL),
		chromedp.ActionFunc(func(ctx context.Context) error {
			// Wait for Fetch.requestPaused handlers to settle (continue/fail).
			done := make(chan struct{})
			go func() {
				pending.Wait()
				close(done)
			}()
			select {
			case <-done:
			case <-ctx.Done():
				return ctx.Err()
			}
			if err := getGuardErr(); err != nil {
				return err
			}
			return boundedLazyLoad(ctx)
		}),
		chromedp.Location(&finalURL),
		chromedp.Title(&title),
		chromedp.OuterHTML("html", &html, chromedp.ByQuery),
	)

	// Fail any still-open main-frame requests without Fetch.disable.
	openReqs.Range(func(key, _ any) bool {
		failPaused(cdpfetch.RequestID(key.(string)))
		return true
	})

	if err := getGuardErr(); err != nil {
		return Page{}, err
	}
	if runErr != nil {
		var fe *Error
		if asFetchError(runErr, &fe) {
			return Page{}, fe
		}
		if ctx.Err() != nil {
			return Page{}, mapCtxErr(ctx.Err())
		}
		if tabCtx.Err() != nil {
			return Page{}, mapCtxErr(tabCtx.Err())
		}
		return Page{}, NewError(CodeBackendError, "cdp capture failed")
	}

	if len([]byte(html)) > MaxCaptureBytes {
		return Page{}, NewError(CodeBackendError, fmt.Sprintf("Captured HTML exceeds the %d MiB limit.", MaxCaptureBytes/(1024*1024)))
	}

	// Close only the owned background tab (tabCancel also closes it).
	_ = chromedp.Run(tabCtx, chromedp.ActionFunc(func(ctx context.Context) error {
		t := chromedp.FromContext(ctx).Target
		if t == nil {
			return nil
		}
		return target.CloseTarget(t.TargetID).Do(cdp.WithExecutor(ctx, chromedp.FromContext(ctx).Browser))
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

func asFetchError(err error, target **Error) bool {
	if err == nil {
		return false
	}
	if e, ok := err.(*Error); ok {
		*target = e
		return true
	}
	return false
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
