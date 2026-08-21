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
	"sync/atomic"
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

// withWSDialer installs dialer as ws.DefaultDialer only for fn (chromedp dials via DefaultDialer).
// The lock is held only for the dial window, not the whole capture.
func withWSDialer(dialer ws.Dialer, fn func() error) error {
	wsDialMu.Lock()
	prev := ws.DefaultDialer
	ws.DefaultDialer = dialer
	defer func() {
		ws.DefaultDialer = prev
		wsDialMu.Unlock()
	}()
	return fn()
}

// Capture opens a background tab, enables main-frame Document Fetch SSRF, navigates,
// waits for network-quiet + DOM signature stability (host timings), optionally
// lazy-loads, reads HTML, and closes only that tab.
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

	dialer := ws.Dialer{}
	if c.APIKey != "" {
		dialer.Header = ws.HandshakeHeaderHTTP(http.Header{
			"Authorization": {"Bearer " + c.APIKey},
		})
	}

	var (
		allocCtx    context.Context
		allocCancel context.CancelFunc
		bootCtx     context.Context
		bootCancel  context.CancelFunc
		bgTargetID  target.ID
	)

	// Dial under a request-local dialer (API key headers). Restore before the long capture.
	if err := withWSDialer(dialer, func() error {
		allocCtx, allocCancel = chromedp.NewRemoteAllocator(ctx, wsURL, chromedp.NoModifyURL)
		bootCtx, bootCancel = chromedp.NewContext(allocCtx)
		return chromedp.Run(bootCtx, chromedp.ActionFunc(func(ctx context.Context) error {
			cc := chromedp.FromContext(ctx)
			browserExec := cdp.WithExecutor(ctx, cc.Browser)
			id, err := target.CreateTarget("about:blank").WithBackground(true).Do(browserExec)
			if err != nil {
				return err
			}
			bgTargetID = id
			if cc.Target != nil && cc.Target.TargetID != "" && cc.Target.TargetID != bgTargetID {
				_ = target.CloseTarget(cc.Target.TargetID).Do(browserExec)
			}
			return nil
		}))
	}); err != nil {
		if allocCancel != nil {
			allocCancel()
		}
		if bootCancel != nil {
			bootCancel()
		}
		if ctx.Err() != nil {
			return Page{}, mapCtxErr(ctx.Err())
		}
		return Page{}, NewError(CodeBackendError, "cdp create target failed")
	}
	defer allocCancel()
	defer bootCancel()

	// Always close the owned background target (success, timeout, SSRF, later failure).
	defer func() {
		if bgTargetID == "" {
			return
		}
		tctx, cancel := context.WithTimeout(context.Background(), time.Duration(TeardownTimeoutMS)*time.Millisecond)
		defer cancel()
		_ = chromedp.Run(bootCtx, chromedp.ActionFunc(func(runCtx context.Context) error {
			cc := chromedp.FromContext(runCtx)
			if cc == nil || cc.Browser == nil {
				return nil
			}
			return target.CloseTarget(bgTargetID).Do(cdp.WithExecutor(tctx, cc.Browser))
		}))
	}()

	tabCtx, tabCancel := chromedp.NewContext(bootCtx, chromedp.WithTargetID(bgTargetID))
	defer tabCancel()

	lookup := c.Lookup
	tracker := NewNetworkTracker(time.Now().UnixMilli())

	var (
		guardMu   sync.Mutex
		guardErr  error
		mainFrame cdp.FrameID
		pending   = NewPendingGate()
		openReqs  sync.Map // requestID → struct{}

		documentGeneration atomic.Int32
		loadCount          atomic.Int32
		loadAtGenMu        sync.Mutex
		loadCountAtGen     = map[int]int{}
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
	throwIfGuard := func() error { return getGuardErr() }

	failPaused := func(requestID cdpfetch.RequestID) {
		tctx, cancel := context.WithTimeout(context.Background(), time.Duration(TeardownTimeoutMS)*time.Millisecond)
		defer cancel()
		cc := chromedp.FromContext(tabCtx)
		if cc == nil || cc.Target == nil {
			return
		}
		exec := cdp.WithExecutor(tctx, cc.Target)
		_ = cdpfetch.FailRequest(requestID, network.ErrorReasonBlockedByClient).Do(exec)
	}

	bumpDocumentGeneration := func() {
		gen := int(documentGeneration.Add(1))
		loadAtGenMu.Lock()
		loadCountAtGen[gen] = int(loadCount.Load())
		loadAtGenMu.Unlock()
	}

	chromedp.ListenTarget(tabCtx, func(ev any) {
		nowMS := time.Now().UnixMilli()
		switch e := ev.(type) {
		case *network.EventRequestWillBeSent:
			if e == nil {
				return
			}
			tracker.OnRequest(string(e.RequestID), string(e.Type), nowMS)
		case *network.EventLoadingFinished:
			if e == nil {
				return
			}
			tracker.OnFinished(string(e.RequestID), nowMS)
		case *network.EventLoadingFailed:
			if e == nil {
				return
			}
			tracker.OnFinished(string(e.RequestID), nowMS)
		case *page.EventLoadEventFired:
			loadCount.Add(1)
		case *page.EventLifecycleEvent:
			if e == nil {
				return
			}
			if (e.Name == "load" || e.Name == "Page.loadEventFired") && e.FrameID == mainFrame {
				loadCount.Add(1)
			}
		case *cdpfetch.EventRequestPaused:
			if e == nil {
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

				isMainDoc := IsMainFrameDocument(resourceType, frameID, mainID)
				if isMainDoc {
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
					return
				}
				if isMainDoc {
					bumpDocumentGeneration()
				}
			}(e)
		}
	})

	setupErr := chromedp.Run(tabCtx, chromedp.ActionFunc(func(ctx context.Context) error {
		if err := page.Enable().Do(ctx); err != nil {
			return err
		}
		if err := page.SetLifecycleEventsEnabled(true).Do(ctx); err != nil {
			return err
		}
		if err := network.Enable().Do(ctx); err != nil {
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
			if err := pending.Wait(ctx); err != nil {
				return err
			}
			if err := getGuardErr(); err != nil {
				return err
			}
			if documentGeneration.Load() == 0 {
				// No main-frame Document intercepted; still allow capture.
				bumpDocumentGeneration()
			}
			return runCapturePipeline(ctx, pending, tracker, &documentGeneration, &loadCount, &loadAtGenMu, loadCountAtGen, throwIfGuard)
		}),
		chromedp.Location(&finalURL),
		chromedp.Title(&title),
		chromedp.OuterHTML("html", &html, chromedp.ByQuery),
	)

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

func runCapturePipeline(
	ctx context.Context,
	pending *PendingGate,
	tracker *NetworkTracker,
	documentGeneration *atomic.Int32,
	loadCount *atomic.Int32,
	loadAtGenMu *sync.Mutex,
	loadCountAtGen map[int]int,
	throwIfGuard func() error,
) error {
	nowFn := func() int64 { return time.Now().UnixMilli() }
	sleepFn := DefaultSleep
	sample := func() (ContentSignature, error) {
		return evaluateSignature(ctx)
	}
	docGen := func() int { return int(documentGeneration.Load()) }
	// Shared operation deadline from the service ctx (e.g. 60s fetch timeout).
	opDeadlineAt := int64(0)
	if dl, ok := ctx.Deadline(); ok {
		opDeadlineAt = dl.UnixMilli()
	}
	waitPending := func() error {
		return pending.Wait(ctx)
	}

	restarts := 0
	for {
		expected := docGen()
		loadAtGenMu.Lock()
		baseline := loadCountAtGen[expected]
		loadAtGenMu.Unlock()

		if err := WaitForLoadMilestone(ctx, struct {
			LoadFired    func() bool
			Now          func() int64
			Sleep        func(ctx context.Context, ms int64) error
			ThrowIfGuard func() error
			DeadlineAt   int64
		}{
			LoadFired:    func() bool { return int(loadCount.Load()) > baseline },
			Now:          nowFn,
			Sleep:        sleepFn,
			ThrowIfGuard: throwIfGuard,
			DeadlineAt:   opDeadlineAt,
		}); err != nil {
			return err
		}

		isRestart := restarts > 0
		readinessMax := int64(ReadinessMaxMS)
		minObserve := int64(ReadinessMinObserveMS)
		if isRestart {
			readinessMax = DocumentRestartReadinessMaxMS
			minObserve = ReadinessMinObserveMS
			if minObserve > DocumentRestartReadinessMaxMS {
				minObserve = DocumentRestartReadinessMaxMS
			}
		}

		sig, err := WaitForReadiness(ctx, WaitForReadinessOpts{
			Sample:              sample,
			Now:                 nowFn,
			Sleep:               sleepFn,
			IsQuiet:             tracker.IsQuiet,
			ThrowIfGuard:        throwIfGuard,
			DocumentGeneration:  docGen,
			ExpectedGeneration:  expected,
			HasExpectedGen:      true,
			MaxMS:               readinessMax,
			MinObserveMS:        minObserve,
			QuietMS:             ReadinessQuietMS,
			SampleMS:            ReadinessSampleMS,
			OperationDeadlineAt: opDeadlineAt,
		})
		if err != nil {
			return err
		}
		if err := throwIfGuard(); err != nil {
			return err
		}

		if !isRestart && docGen() == expected {
			sig, err = boundedLazyLoadPipeline(ctx, tracker, sig, sample, nowFn, sleepFn, throwIfGuard, docGen, expected, opDeadlineAt)
			if err != nil {
				return err
			}
			_ = sig
			if err := throwIfGuard(); err != nil {
				return err
			}
		}

		if err := waitPending(); err != nil {
			return err
		}
		if err := throwIfGuard(); err != nil {
			return err
		}

		if docGen() == expected {
			return nil
		}
		restarts++
		if restarts > DocumentRestartMax {
			return nil
		}
		if opDeadlineAt > 0 && nowFn() >= opDeadlineAt {
			return nil
		}
	}
}

func boundedLazyLoadPipeline(
	ctx context.Context,
	tracker *NetworkTracker,
	initial ContentSignature,
	sample func() (ContentSignature, error),
	nowFn func() int64,
	sleepFn func(ctx context.Context, ms int64) error,
	throwIfGuard func() error,
	docGen func() int,
	expected int,
	opDeadlineAt int64,
) (ContentSignature, error) {
	phaseStarted := nowFn()
	phaseBudget := budgetMS(opDeadlineAt, LazyLoadMaxMS, phaseStarted)
	if phaseBudget <= 0 {
		return initial, nil
	}

	sig := initial
	noGrowth := 0
	scrolledViewports := 0.0
	stepsCompleted := 0

	for {
		if err := ctx.Err(); err != nil {
			return sig, err
		}
		if err := throwIfGuard(); err != nil {
			return sig, err
		}
		if docGen() != expected {
			return sig, nil
		}

		phaseElapsed := nowFn() - phaseStarted
		if LazyLoadShouldStop(LazyLoadBudgetState{
			StepsCompleted:    stepsCompleted,
			ScrolledViewports: scrolledViewports,
			NoGrowth:          noGrowth,
			PhaseElapsedMS:    phaseElapsed,
			CanScroll:         sig.CanScroll,
			NearHTMLCap:       IsNearHTMLCap(sig),
		}) {
			return sig, nil
		}

		before := sig
		var scrollResult map[string]any
		if err := chromedp.Evaluate(ScrollExpression(), &scrollResult).Do(ctx); err != nil {
			return sig, nil
		}
		stepsCompleted++
		scrolledViewports += LazyLoadStepViewports

		settleBudget := int64(LazyLoadSettleMaxMS)
		remain := phaseBudget - (nowFn() - phaseStarted)
		if remain < settleBudget {
			settleBudget = remain
		}
		if opDeadlineAt > 0 {
			opRemain := opDeadlineAt - nowFn()
			if opRemain < settleBudget {
				settleBudget = opRemain
			}
		}
		if settleBudget > 0 {
			minObs := int64(ReadinessQuietMS)
			if minObs > settleBudget {
				minObs = settleBudget
			}
			quiet := int64(ReadinessQuietMS)
			if quiet > settleBudget {
				quiet = settleBudget
			}
			next, err := WaitForReadiness(ctx, WaitForReadinessOpts{
				Sample:              sample,
				Now:                 nowFn,
				Sleep:               sleepFn,
				IsQuiet:             tracker.IsQuiet,
				ThrowIfGuard:        throwIfGuard,
				DocumentGeneration:  docGen,
				ExpectedGeneration:  expected,
				HasExpectedGen:      true,
				MaxMS:               settleBudget,
				MinObserveMS:        minObs,
				QuietMS:             quiet,
				SampleMS:            ReadinessSampleMS,
				OperationDeadlineAt: opDeadlineAt,
			})
			if err != nil {
				return sig, err
			}
			sig = next
		} else {
			next, err := sample()
			if err != nil {
				return sig, err
			}
			sig = next
		}

		if docGen() != expected {
			return sig, nil
		}
		if HasMeaningfulGrowth(before, sig) {
			noGrowth = 0
		} else {
			noGrowth++
		}
	}
}

func evaluateSignature(ctx context.Context) (ContentSignature, error) {
	var raw map[string]any
	if err := chromedp.Evaluate(SignatureExpression, &raw).Do(ctx); err != nil {
		return ContentSignature{}, err
	}
	return parseSignatureMap(raw), nil
}

func parseSignatureMap(raw map[string]any) ContentSignature {
	num := func(key string) int {
		switch v := raw[key].(type) {
		case float64:
			return int(v)
		case int:
			return v
		case json.Number:
			i, _ := v.Int64()
			return int(i)
		default:
			return 0
		}
	}
	canScroll, _ := raw["canScroll"].(bool)
	return ContentSignature{
		TextLength:   num("textLength"),
		ArticleCount: num("articleCount"),
		LinkCount:    num("linkCount"),
		HeadingCount: num("headingCount"),
		ScrollHeight: num("scrollHeight"),
		HTMLChars:    num("htmlChars"),
		CanScroll:    canScroll,
	}
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

// ResolveCDPWebsocketURLForTest exports resolveCDPWebsocketURL for unit tests.
func ResolveCDPWebsocketURLForTest(ctx context.Context, endpoint, apiKey string) (string, error) {
	return resolveCDPWebsocketURL(ctx, endpoint, apiKey)
}

func resolveCDPWebsocketURL(ctx context.Context, endpoint, apiKey string) (string, error) {
	trimmed := strings.TrimSpace(endpoint)
	if trimmed == "" {
		return "", fmt.Errorf("empty cdp endpoint")
	}
	lower := strings.ToLower(trimmed)
	// Already a DevTools websocket (or any /devtools/ path) — use as-is.
	if strings.HasPrefix(lower, "ws:") || strings.HasPrefix(lower, "wss:") || strings.Contains(lower, "/devtools/") {
		return trimmed, nil
	}

	u, err := url.Parse(trimmed)
	if err != nil {
		return "", err
	}
	switch u.Scheme {
	case "http", "https":
		// Chrome / cloakserve HTTP root is /json/version. Keep a non-root
		// path as-is (custom discovery URLs).
		if u.Path == "" || u.Path == "/" {
			u.Path = "/json/version"
		}
	default:
		return "", fmt.Errorf("unsupported cdp endpoint scheme")
	}

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
