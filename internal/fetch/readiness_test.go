package fetch_test

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/sidkang/webgate/internal/fetch"
)

func baseSig() fetch.ContentSignature {
	return fetch.ContentSignature{
		TextLength:   100,
		ArticleCount: 1,
		LinkCount:    2,
		HeadingCount: 1,
		ScrollHeight: 1000,
		HTMLChars:    1000,
		CanScroll:    true,
	}
}

func TestHasMeaningfulGrowthAndNearCap(t *testing.T) {
	base := baseSig()
	if !fetch.HasMeaningfulGrowth(base, fetch.ContentSignature{
		TextLength: base.TextLength, ArticleCount: 2, LinkCount: base.LinkCount,
		HeadingCount: base.HeadingCount, ScrollHeight: base.ScrollHeight, HTMLChars: base.HTMLChars,
	}) {
		t.Fatal("article growth")
	}
	if fetch.HasMeaningfulGrowth(base, fetch.ContentSignature{
		TextLength: 250, ArticleCount: base.ArticleCount, LinkCount: base.LinkCount,
		HeadingCount: base.HeadingCount, ScrollHeight: base.ScrollHeight, HTMLChars: base.HTMLChars,
	}) {
		t.Fatal("250 text should not grow (need +200 from 100 → 300)")
	}
	if !fetch.HasMeaningfulGrowth(base, fetch.ContentSignature{
		TextLength: 400, ArticleCount: base.ArticleCount, LinkCount: base.LinkCount,
		HeadingCount: base.HeadingCount, ScrollHeight: base.ScrollHeight, HTMLChars: base.HTMLChars,
	}) {
		t.Fatal("400 text growth")
	}
	near := base
	near.HTMLChars = fetch.HTMLSoftCharLimit
	if !fetch.IsNearHTMLCap(near) {
		t.Fatal("at soft cap")
	}
	near.HTMLChars = fetch.HTMLSoftCharLimit - 1
	if fetch.IsNearHTMLCap(near) {
		t.Fatal("below soft cap")
	}
	if fetch.IsNearHTMLCap(base) {
		t.Fatal("base not near cap")
	}
}

func TestLazyLoadShouldStopBudgets(t *testing.T) {
	base := fetch.LazyLoadBudgetState{
		StepsCompleted:    0,
		ScrolledViewports: 0,
		NoGrowth:          0,
		PhaseElapsedMS:    0,
		CanScroll:         true,
		NearHTMLCap:       false,
	}
	if fetch.LazyLoadShouldStop(base) {
		t.Fatal("base should continue")
	}
	s := base
	s.StepsCompleted = fetch.LazyLoadMaxSteps
	if !fetch.LazyLoadShouldStop(s) {
		t.Fatal("max steps")
	}
	s = base
	s.ScrolledViewports = fetch.LazyLoadMaxDistanceViewports
	if !fetch.LazyLoadShouldStop(s) {
		t.Fatal("max distance")
	}
	s = base
	s.NoGrowth = 2
	if !fetch.LazyLoadShouldStop(s) {
		t.Fatal("no growth")
	}
	s = base
	s.PhaseElapsedMS = 5_000
	if !fetch.LazyLoadShouldStop(s) {
		t.Fatal("phase elapsed")
	}
	s = base
	s.CanScroll = false
	if !fetch.LazyLoadShouldStop(s) {
		t.Fatal("cannot scroll")
	}
	s = base
	s.NearHTMLCap = true
	if !fetch.LazyLoadShouldStop(s) {
		t.Fatal("near html cap")
	}
	if !(fetch.LazyLoadMaxDistanceViewports < float64(fetch.LazyLoadMaxSteps)*fetch.LazyLoadStepViewports) {
		t.Fatal("distance should bind before steps under continuous growth")
	}
}

func TestNetworkTrackerQuietAndRelevantTypes(t *testing.T) {
	tr := fetch.NewNetworkTracker(0)
	tr.OnRequest("img", "Image", 10)
	if !tr.IsQuiet(0, 10) {
		t.Fatal("image should be ignored")
	}
	tr.OnRequest("a", "XHR", 20)
	if tr.IsQuiet(0, 20) {
		t.Fatal("xhr inflight")
	}
	tr.OnFinished("a", 100)
	if tr.IsQuiet(750, 100) {
		t.Fatal("not quiet yet")
	}
	if !tr.IsQuiet(750, 850) {
		t.Fatal("quiet after window")
	}
	// Missing type counts as relevant.
	tr.OnRequest("b", "", 900)
	if tr.IsQuiet(0, 900) {
		t.Fatal("missing type should be tracked")
	}
}

func TestWaitForReadinessRequiresDOMStableAndNetworkQuiet(t *testing.T) {
	var now int64
	var sampleN atomic.Int32
	sigs := []fetch.ContentSignature{
		{TextLength: 10, HTMLChars: 100},
		{TextLength: 50, HTMLChars: 200}, // changes on 2nd → reset stable
		{TextLength: 50, HTMLChars: 200},
		{TextLength: 50, HTMLChars: 200},
		{TextLength: 50, HTMLChars: 200},
		{TextLength: 50, HTMLChars: 200},
		{TextLength: 50, HTMLChars: 200},
		{TextLength: 50, HTMLChars: 200},
		{TextLength: 50, HTMLChars: 200},
		{TextLength: 50, HTMLChars: 200},
	}
	tr := fetch.NewNetworkTracker(0)
	tr.OnRequest("boot", "Fetch", 0) // inflight until 3rd sleep tick

	sleepN := 0
	got, err := fetch.WaitForReadiness(context.Background(), fetch.WaitForReadinessOpts{
		MaxMS:        10_000,
		MinObserveMS: 1_500,
		QuietMS:      750,
		SampleMS:     250,
		Now:          func() int64 { return now },
		Sleep: func(ctx context.Context, ms int64) error {
			sleepN++
			now += ms
			// Network goes quiet on the 3rd sleep (after samples have started stabilizing).
			if sleepN == 3 {
				tr.OnFinished("boot", now)
			}
			return nil
		},
		Sample: func() (fetch.ContentSignature, error) {
			i := int(sampleN.Add(1) - 1)
			if i >= len(sigs) {
				i = len(sigs) - 1
			}
			return sigs[i], nil
		},
		IsQuiet: tr.IsQuiet,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.TextLength != 50 {
		t.Fatalf("got=%+v", got)
	}
	// Must have waited at least minObserve (1500); quiet windows are satisfied at that point.
	if now < 1500 {
		t.Fatalf("returned too early now=%d sleeps=%d samples=%d", now, sleepN, sampleN.Load())
	}
	if !tr.IsQuiet(750, now) {
		t.Fatal("expected quiet network at return")
	}
}

func TestWaitForReadinessNearCapReturnsEarly(t *testing.T) {
	now := int64(0)
	got, err := fetch.WaitForReadiness(context.Background(), fetch.WaitForReadinessOpts{
		MaxMS:        8_000,
		MinObserveMS: 1_500,
		QuietMS:      750,
		SampleMS:     250,
		Now:          func() int64 { return now },
		Sleep: func(ctx context.Context, ms int64) error {
			now += ms
			return nil
		},
		Sample: func() (fetch.ContentSignature, error) {
			return fetch.ContentSignature{HTMLChars: fetch.HTMLSoftCharLimit, TextLength: 1}, nil
		},
		IsQuiet: func(quietMS, nowMS int64) bool { return false }, // network never quiet
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.HTMLChars != fetch.HTMLSoftCharLimit {
		t.Fatalf("%+v", got)
	}
	if now != 0 {
		t.Fatalf("should return on first sample without sleeping, now=%d", now)
	}
}
