package fetch_test

import (
	"context"
	"testing"

	"github.com/sidkang/webgate/internal/fetch"
)

func TestParseFetchURL(t *testing.T) {
	u, reason, _ := fetch.ParseFetchURL(" https://example.com/path ")
	if reason != "" || u == nil || u.String() != "https://example.com/path" {
		t.Fatalf("got reason=%q url=%v", reason, u)
	}
	cases := []string{
		"file:///tmp/x",
		"https://localhost/x",
		"https://app.localhost/x",
		"http://127.0.0.1/",
		"http://169.254.169.254/latest",
		"http://[::1]/",
		"http://[::ffff:127.0.0.1]/",
		"http://[::ffff:7f00:1]/",
		"http://[0:0:0:0:0:ffff:127.0.0.1]/",
		"http://[fe80::1]/",
		"http://[fe90::1]/",
		"http://[febf::1]/",
		"http://metadata/",
		"http://metadata.google.internal/",
	}
	for _, in := range cases {
		if _, reason, _ := fetch.ParseFetchURL(in); reason == "" {
			t.Fatalf("expected block for %q", in)
		}
	}
	// 198.18 is not specially denied
	if _, reason, _ := fetch.ParseFetchURL("http://198.18.0.1/"); reason != "" {
		t.Fatalf("198.18 should parse: %s", reason)
	}
}

func TestIsBlockedIPAddress(t *testing.T) {
	blocked := []string{"10.0.0.1", "192.168.1.1", "172.16.0.1", "169.254.169.254", "::ffff:7f00:1", "0:0:0:0:0:ffff:127.0.0.1", "fe80::1", "fe90::1", "febf::abcd"}
	for _, ip := range blocked {
		if !fetch.IsBlockedIPAddress(ip) {
			t.Fatalf("expected blocked %s", ip)
		}
	}
	if fetch.IsBlockedIPAddress("8.8.8.8") {
		t.Fatal("8.8.8.8 should be allowed")
	}
	if fetch.IsBlockedIPAddress("198.18.0.1") {
		t.Fatal("198.18.0.1 should not be specially blocked")
	}
	// fec0 outside fe80::/10
	if fetch.IsBlockedIPAddress("fec0::1") {
		t.Fatal("fec0::1 should not be blocked by link-local rule")
	}
}

func TestAssertFetchURLAllowedDNS(t *testing.T) {
	ctx := context.Background()
	_, err := fetch.AssertFetchURLAllowed(ctx, "https://private.example", func(context.Context, string) ([]fetch.LookupAddress, error) {
		return []fetch.LookupAddress{{Address: "10.1.2.3"}}, nil
	})
	if err == nil {
		t.Fatal("expected blocked DNS")
	}
	u, err := fetch.AssertFetchURLAllowed(ctx, "https://public.example", func(context.Context, string) ([]fetch.LookupAddress, error) {
		return []fetch.LookupAddress{{Address: "1.1.1.1"}}, nil
	})
	if err != nil || u.Hostname() != "public.example" {
		t.Fatalf("public: %v %v", u, err)
	}
	// lookup failure allows
	u, err = fetch.AssertFetchURLAllowed(ctx, "https://unknown.example", func(context.Context, string) ([]fetch.LookupAddress, error) {
		return nil, context.DeadlineExceeded
	})
	// DeadlineExceeded on lookup with non-canceled parent... mapCtxErr only if ctx.Err()
	// Our AssertFetchURLAllowed: if lookup fails and ctx.Err() is nil, allow.
	// But we returned context.DeadlineExceeded as lookup error - ctx itself is not done.
	if err != nil {
		t.Fatalf("lookup failure should allow, got %v", err)
	}
	if u.Hostname() != "unknown.example" {
		t.Fatalf("hostname=%s", u.Hostname())
	}
}
