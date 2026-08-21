package fetch

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Host CDP readiness / lazy-load timings (cdp-fetch.ts).
const (
	LoadEventWaitMS               = 5_000
	ReadinessMinObserveMS         = 1_500
	ReadinessQuietMS              = 750
	ReadinessSampleMS             = 250
	ReadinessMaxMS                = 8_000
	LazyLoadSettleMaxMS           = 1_500
	LazyLoadNoGrowthLimit         = 2
	LazyLoadMinTextGrowth         = 200
	HTMLSoftCharLimit             = 56_000_000
	DocumentRestartMax            = 2
	DocumentRestartReadinessMaxMS = 3_000
)

// ContentSignature is the host DOM/content fingerprint used for readiness.
type ContentSignature struct {
	TextLength   int
	ArticleCount int
	LinkCount    int
	HeadingCount int
	ScrollHeight int
	HTMLChars    int
	CanScroll    bool
}

// LazyLoadBudgetState is the pure stop-rule input for the lazy-load loop.
type LazyLoadBudgetState struct {
	StepsCompleted    int
	ScrolledViewports float64
	NoGrowth          int
	PhaseElapsedMS    int64
	CanScroll         bool
	NearHTMLCap       bool
}

// SignatureKey is a stable fingerprint of DOM metrics (excludes CanScroll).
func SignatureKey(s ContentSignature) string {
	return fmt.Sprintf("%d|%d|%d|%d|%d|%d",
		s.TextLength, s.ArticleCount, s.LinkCount, s.HeadingCount, s.ScrollHeight, s.HTMLChars)
}

// HasMeaningfulGrowth mirrors host hasMeaningfulGrowth.
func HasMeaningfulGrowth(before, after ContentSignature) bool {
	if after.ArticleCount > before.ArticleCount {
		return true
	}
	if after.LinkCount > before.LinkCount {
		return true
	}
	if after.HeadingCount > before.HeadingCount {
		return true
	}
	if after.TextLength >= before.TextLength+LazyLoadMinTextGrowth {
		return true
	}
	return false
}

// IsNearHTMLCap is the soft pre-stop before the 64 MiB hard capture cap.
func IsNearHTMLCap(s ContentSignature) bool {
	return s.HTMLChars >= HTMLSoftCharLimit
}

// LazyLoadShouldStop proves each hard lazy-load budget independently.
func LazyLoadShouldStop(state LazyLoadBudgetState) bool {
	if state.PhaseElapsedMS >= LazyLoadMaxMS {
		return true
	}
	if state.StepsCompleted >= LazyLoadMaxSteps {
		return true
	}
	if state.ScrolledViewports >= LazyLoadMaxDistanceViewports {
		return true
	}
	if state.NoGrowth >= LazyLoadNoGrowthLimit {
		return true
	}
	if !state.CanScroll {
		return true
	}
	if state.NearHTMLCap {
		return true
	}
	return false
}

// IsRelevantResourceType reports network types that affect main-content readiness.
// Missing type is treated as relevant by NetworkTracker.OnRequest.
func IsRelevantResourceType(resourceType string) bool {
	switch resourceType {
	case "Document", "XHR", "Fetch", "Script":
		return true
	default:
		return false
	}
}

// NetworkTracker tracks inflight relevant network requests for quiet windows.
type NetworkTracker struct {
	mu             sync.Mutex
	inflight       map[string]struct{}
	lastActivityAt int64 // unix ms
}

// NewNetworkTracker starts with lastActivityAt = nowMS.
func NewNetworkTracker(nowMS int64) *NetworkTracker {
	return &NetworkTracker{
		inflight:       map[string]struct{}{},
		lastActivityAt: nowMS,
	}
}

// OnRequest records a relevant (or type-omitted) request start.
func (t *NetworkTracker) OnRequest(id, resourceType string, nowMS int64) {
	if id == "" {
		return
	}
	if resourceType != "" && !IsRelevantResourceType(resourceType) {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.inflight[id] = struct{}{}
	t.lastActivityAt = nowMS
}

// OnFinished records request completion / failure.
func (t *NetworkTracker) OnFinished(id string, nowMS int64) {
	if id == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, ok := t.inflight[id]; !ok {
		return
	}
	delete(t.inflight, id)
	t.lastActivityAt = nowMS
}

// IsQuiet is true when nothing is inflight and quietMs has elapsed since last activity.
func (t *NetworkTracker) IsQuiet(quietMS, nowMS int64) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.inflight) == 0 && nowMS-t.lastActivityAt >= quietMS
}

// WaitForReadinessOpts is the injectable readiness loop (host waitForReadiness).
type WaitForReadinessOpts struct {
	Sample              func() (ContentSignature, error)
	Now                 func() int64 // unix ms
	Sleep               func(ctx context.Context, ms int64) error
	IsQuiet             func(quietMS, nowMS int64) bool
	ThrowIfGuard        func() error
	DocumentGeneration  func() int
	ExpectedGeneration  int
	HasExpectedGen      bool
	MaxMS               int64
	MinObserveMS        int64
	QuietMS             int64
	SampleMS            int64
	OperationDeadlineAt int64 // 0 = none
}

func budgetMS(operationDeadlineAt, preferred, now int64) int64 {
	if operationDeadlineAt <= 0 {
		return preferred
	}
	remaining := operationDeadlineAt - now
	if remaining < 0 {
		remaining = 0
	}
	if preferred < remaining {
		return preferred
	}
	return remaining
}

// WaitForReadiness samples until DOM-stable + network-quiet (or near HTML cap / max).
func WaitForReadiness(ctx context.Context, opts WaitForReadinessOpts) (ContentSignature, error) {
	if opts.Sample == nil || opts.Now == nil || opts.Sleep == nil || opts.IsQuiet == nil {
		return ContentSignature{}, fmt.Errorf("waitForReadiness: missing injection")
	}
	if opts.ThrowIfGuard == nil {
		opts.ThrowIfGuard = func() error { return nil }
	}
	if opts.SampleMS <= 0 {
		opts.SampleMS = ReadinessSampleMS
	}
	startedAt := opts.Now()
	phaseBudget := budgetMS(opts.OperationDeadlineAt, opts.MaxMS, startedAt)
	phaseDeadlineAt := startedAt + phaseBudget

	previousKey := ""
	stableSince := startedAt
	var last ContentSignature
	haveLast := false

	for opts.Now() < phaseDeadlineAt {
		if err := ctx.Err(); err != nil {
			return last, err
		}
		if err := opts.ThrowIfGuard(); err != nil {
			return last, err
		}
		if opts.HasExpectedGen && opts.DocumentGeneration != nil &&
			opts.DocumentGeneration() != opts.ExpectedGeneration {
			if haveLast {
				return last, nil
			}
			return opts.Sample()
		}

		sig, err := opts.Sample()
		if err != nil {
			return last, err
		}
		last = sig
		haveLast = true
		key := SignatureKey(sig)
		observedAt := opts.Now()
		if key != previousKey {
			previousKey = key
			stableSince = observedAt
		}

		observedLongEnough := observedAt-startedAt >= opts.MinObserveMS
		domQuiet := observedAt-stableSince >= opts.QuietMS
		networkQuiet := opts.IsQuiet(opts.QuietMS, observedAt)
		if observedLongEnough && domQuiet && networkQuiet {
			return sig, nil
		}
		if IsNearHTMLCap(sig) {
			return sig, nil
		}

		remaining := phaseDeadlineAt - opts.Now()
		if remaining <= 0 {
			break
		}
		slice := opts.SampleMS
		if slice > remaining {
			slice = remaining
		}
		if err := opts.Sleep(ctx, slice); err != nil {
			return last, err
		}
	}

	if haveLast {
		return last, nil
	}
	return opts.Sample()
}

// WaitForLoadMilestone polls until loadFired or LoadEventWaitMS (soft; not a hard failure).
func WaitForLoadMilestone(ctx context.Context, opts struct {
	LoadFired    func() bool
	Now          func() int64
	Sleep        func(ctx context.Context, ms int64) error
	ThrowIfGuard func() error
	DeadlineAt   int64 // optional outer deadline; 0 = none
}) error {
	if opts.ThrowIfGuard == nil {
		opts.ThrowIfGuard = func() error { return nil }
	}
	loadWait := budgetMS(opts.DeadlineAt, LoadEventWaitMS, opts.Now())
	loadDeadline := opts.Now() + loadWait
	for !opts.LoadFired() && opts.Now() < loadDeadline {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := opts.ThrowIfGuard(); err != nil {
			return err
		}
		slice := int64(50)
		remain := loadDeadline - opts.Now()
		if remain < slice {
			slice = remain
		}
		if slice <= 0 {
			break
		}
		if err := opts.Sleep(ctx, slice); err != nil {
			return err
		}
	}
	return opts.ThrowIfGuard()
}

// SignatureExpression is the host SIGNATURE_EXPRESSION for Runtime.evaluate.
// htmlChars uses the cheap textLength signal (not outerHTML) so the 250ms
// readiness loop does not serialize the full document repeatedly.
const SignatureExpression = `(() => {
	const root = document.querySelector("main")
		|| document.querySelector('[role="main"]')
		|| document.body
		|| document.documentElement;
	const textLength = root && typeof root.innerText === "string" ? root.innerText.length : 0;
	const articleCount = root ? root.querySelectorAll("article").length : 0;
	const linkCount = root ? root.querySelectorAll("a[href]").length : 0;
	const headingCount = root ? root.querySelectorAll("h1,h2,h3").length : 0;
	const scrollHeight = Math.max(
		document.documentElement ? document.documentElement.scrollHeight : 0,
		document.body ? document.body.scrollHeight : 0,
	);
	const viewport = window.innerHeight || 0;
	const y = window.scrollY || document.documentElement.scrollTop || 0;
	const canScroll = scrollHeight > viewport + y + 4;
	const htmlChars = textLength;
	return { textLength, articleCount, linkCount, headingCount, scrollHeight, canScroll, htmlChars };
})()`

// ScrollExpression scrolls by LazyLoadStepViewports of the viewport (host SCROLL_EXPRESSION).
func ScrollExpression() string {
	return fmt.Sprintf(`(() => {
	const viewport = Math.max(1, window.innerHeight || 0);
	const step = Math.floor(viewport * %g);
	window.scrollBy(0, step);
	return {
		scrolledBy: step,
		scrollY: window.scrollY || document.documentElement.scrollTop || 0,
		scrollHeight: Math.max(
			document.documentElement ? document.documentElement.scrollHeight : 0,
			document.body ? document.body.scrollHeight : 0,
		),
	};
})()`, LazyLoadStepViewports)
}

// DefaultSleep sleeps ms milliseconds respecting ctx.
func DefaultSleep(ctx context.Context, ms int64) error {
	if ms <= 0 {
		return nil
	}
	t := time.NewTimer(time.Duration(ms) * time.Millisecond)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
