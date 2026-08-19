package search_test

import (
	"context"
	"errors"
	"net"
	"net/url"
	"testing"

	"github.com/sidkang/webgate/internal/search"
)

func TestClassifyTypedCodes(t *testing.T) {
	cases := []struct {
		code search.Code
		kind search.FallbackKind
	}{
		{search.CodeRateLimited, search.KindContinue},
		{search.CodeBackendError, search.KindContinue},
		{search.CodeTimeout, search.KindContinue},
		{search.CodeInvalidResponse, search.KindContinue},
		{search.CodeAborted, search.KindStop},
		{search.CodeInvalidInput, search.KindStop},
		{search.CodeAuthFailed, search.KindStop},
		{search.CodeMissingConfig, search.KindStop},
		{search.CodeUnsupportedModel, search.KindStop},
		{search.CodeUnsupportedTool, search.KindStop},
		{search.CodeUnsupportedToolChoice, search.KindStop},
	}
	for _, tc := range cases {
		got := search.Classify(search.NewError(tc.code, "x"))
		if got.Kind != tc.kind || got.Code != tc.code {
			t.Fatalf("%s: got kind=%s code=%s", tc.code, got.Kind, got.Code)
		}
	}
}

type timeoutNetError struct{}

func (timeoutNetError) Error() string   { return "i/o timeout" }
func (timeoutNetError) Timeout() bool   { return true }
func (timeoutNetError) Temporary() bool { return true }

func TestClassifyContextAndNetwork(t *testing.T) {
	if got := search.Classify(context.Canceled); got.Kind != search.KindStop || got.Code != search.CodeAborted {
		t.Fatalf("canceled: %+v", got)
	}
	if got := search.Classify(context.DeadlineExceeded); got.Kind != search.KindContinue || got.Code != search.CodeTimeout {
		t.Fatalf("deadline: %+v", got)
	}
	netErr := &net.OpError{Op: "dial", Err: errors.New("connection refused")}
	if got := search.Classify(netErr); got.Kind != search.KindContinue || got.Code != search.CodeBackendError {
		t.Fatalf("net: %+v", got)
	}
	urlErr := &url.Error{Op: "Get", URL: "http://x", Err: netErr}
	if got := search.Classify(urlErr); got.Kind != search.KindContinue || got.Code != search.CodeBackendError {
		t.Fatalf("url: %+v", got)
	}
	if got := search.Classify(errors.New("surprise")); got.Kind != search.KindStop || got.Code != search.CodeBackendError {
		t.Fatalf("generic: %+v", got)
	}
}

func TestClassifyNetTimeoutIsTimeout(t *testing.T) {
	got := search.Classify(timeoutNetError{})
	if got.Kind != search.KindContinue || got.Code != search.CodeTimeout {
		t.Fatalf("timeout net.Error: %+v", got)
	}
	wrapped := &url.Error{Op: "Get", URL: "http://x", Err: timeoutNetError{}}
	got = search.Classify(wrapped)
	if got.Kind != search.KindContinue || got.Code != search.CodeTimeout {
		t.Fatalf("wrapped timeout: %+v", got)
	}
}

func TestParseProvider(t *testing.T) {
	plan, err := search.ParseProvider(nil)
	if err != nil || !plan.SkipMissing || len(plan.Chain) != 2 || plan.Chain[0] != "openai" {
		t.Fatalf("nil: %+v err=%v", plan, err)
	}
	plan, err = search.ParseProvider("auto")
	if err != nil || plan.Chain[0] != "searxng" || !plan.SkipMissing {
		t.Fatalf("auto: %+v err=%v", plan, err)
	}
	plan, err = search.ParseProvider("searxng")
	if err != nil || plan.SkipMissing || len(plan.Chain) != 1 {
		t.Fatalf("named: %+v err=%v", plan, err)
	}
	if _, err := search.ParseProvider("google"); err == nil || err.Code != search.CodeInvalidInput {
		t.Fatalf("google: %v", err)
	}
	if _, err := search.ParseProvider([]any{"openai"}); err == nil || err.Code != search.CodeInvalidInput {
		t.Fatalf("array: %v", err)
	}
}
