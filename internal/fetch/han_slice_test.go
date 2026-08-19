package fetch_test

import (
	"strings"
	"testing"

	"github.com/sidkang/webgate/internal/fetch"
)

func TestNormalizeHanTypography(t *testing.T) {
	plain := `<html><body><p>Hello world</p></body></html>`
	if got := fetch.NormalizeHanTypographyHTML(plain); got != plain {
		t.Fatalf("plain changed: %q", got)
	}

	in := `<p>Hello<h-char unicode="ff0c" class="biaodian"><h-inner>，</h-inner></h-char>world<h-hws> </h-hws>!</p>`
	out := fetch.NormalizeHanTypographyHTML(in)
	if strings.Contains(out, "h-char") || strings.Contains(out, "h-inner") || strings.Contains(out, "h-hws") {
		t.Fatalf("wrappers remain: %s", out)
	}
	if !strings.Contains(out, "，") || !strings.Contains(out, "Hello") || !strings.Contains(out, "world") {
		t.Fatalf("text lost: %s", out)
	}
}

func TestInitialContentSlice(t *testing.T) {
	if fetch.NormalizeMaxInlineChars(0) != fetch.DefaultMaxInlineChars {
		t.Fatal("default")
	}
	if fetch.NormalizeMaxInlineChars(500_000) != fetch.MaxInlineChars {
		t.Fatal("cap")
	}
	var b strings.Builder
	for i := 0; i < 100; i++ {
		b.WriteString("line\n")
	}
	content := b.String()
	slice := fetch.InitialContentSlice(content, 80)
	if !slice.Truncated {
		t.Fatal("expected truncated")
	}
	if slice.ReturnedChars > 80 {
		t.Fatalf("returned=%d", slice.ReturnedChars)
	}
	notice := fetch.TruncationNotice(slice.ReturnedChars, slice.TotalChars)
	if !strings.Contains(notice, "omitted tail is not retrievable") {
		t.Fatalf("notice=%q", notice)
	}
}

// TestInitialContentSliceCJKNewlines would panic when byte indexes were mixed into []rune.
func TestInitialContentSliceCJKNewlines(t *testing.T) {
	var b strings.Builder
	// Enough CJK+newlines that UTF-8 byte length >> rune length past both limits.
	for i := 0; i < 20_000; i++ {
		b.WriteString("中文内容段落测试")
		b.WriteByte('\n')
	}
	content := b.String()
	totalRunes := len([]rune(content))
	for _, limit := range []int{50, 30_000} {
		slice := fetch.InitialContentSlice(content, limit)
		cap := fetch.NormalizeMaxInlineChars(limit)
		if slice.ReturnedChars > cap {
			t.Fatalf("limit=%d returned=%d", limit, slice.ReturnedChars)
		}
		if totalRunes <= cap {
			t.Fatalf("fixture too small for limit=%d total=%d", limit, totalRunes)
		}
		if !slice.Truncated {
			t.Fatalf("limit=%d expected truncated total=%d", limit, slice.TotalChars)
		}
		if slice.TotalChars != totalRunes {
			t.Fatalf("limit=%d total=%d want=%d", limit, slice.TotalChars, totalRunes)
		}
		// Must remain valid UTF-8 / convertible without panic.
		_ = []rune(slice.Text)
	}
}
