package fetch

import (
	"fmt"
	"strconv"
)

const (
	DefaultMaxInlineChars = 30_000
	MaxInlineChars        = 200_000
	FetchTimeoutMS        = 60_000
	MaxCaptureBytes       = 64 * 1024 * 1024

	LazyLoadMaxSteps             = 4
	LazyLoadStepViewports        = 0.8
	LazyLoadMaxDistanceViewports = 2.4
	LazyLoadMaxMS                = 5_000

	TeardownTimeoutMS = 2_000
)

// NormalizeMaxInlineChars applies the host defaults and hard cap.
func NormalizeMaxInlineChars(value int) int {
	if value <= 0 {
		return DefaultMaxInlineChars
	}
	if value > MaxInlineChars {
		return MaxInlineChars
	}
	return value
}

// ContentSlice is the truncated inline payload.
type ContentSlice struct {
	Text          string
	TotalChars    int
	ReturnedChars int
	Truncated     bool
}

// InitialContentSlice truncates content like host fetch-content initialContentSlice.
// Counting and newline snap are done entirely in rune space (not UTF-8 byte indexes),
// so CJK content cannot panic by mixing byte offsets into a []rune slice.
func InitialContentSlice(content string, maxChars int) ContentSlice {
	limit := NormalizeMaxInlineChars(maxChars)
	runes := []rune(content)
	total := len(runes)
	endOffset := total
	if endOffset > limit {
		endOffset = limit
		if endOffset < total {
			// Prefer breaking on a newline at or after 80% of the limit (rune indexes).
			minBreak := limit * 8 / 10
			for i := endOffset - 1; i >= minBreak; i-- {
				if runes[i] == '\n' {
					endOffset = i + 1
					break
				}
			}
		}
	}
	text := string(runes[:endOffset])
	return ContentSlice{
		Text:          text,
		TotalChars:    total,
		ReturnedChars: endOffset,
		Truncated:     endOffset < total,
	}
}

// TruncationNotice is appended when content is truncated (host wording).
func TruncationNotice(returned, total int) string {
	return fmt.Sprintf("\n\n---\nShowing %s of %s characters. The omitted tail is not retrievable.",
		strconv.Itoa(returned), strconv.Itoa(total))
}
