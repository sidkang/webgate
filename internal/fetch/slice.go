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
func InitialContentSlice(content string, maxChars int) ContentSlice {
	limit := NormalizeMaxInlineChars(maxChars)
	runes := []rune(content)
	total := len(runes)
	endOffset := total
	if endOffset > limit {
		endOffset = limit
		if endOffset < total {
			window := string(runes[:endOffset])
			lineBreak := lastIndexByte(window, '\n')
			if lineBreak >= limit*8/10 {
				endOffset = lineBreak + 1
			}
		}
	}
	text := string(runes[:endOffset])
	return ContentSlice{
		Text:          text,
		TotalChars:    total,
		ReturnedChars: len([]rune(text)),
		Truncated:     endOffset < total,
	}
}

func lastIndexByte(s string, c byte) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == c {
			return i
		}
	}
	return -1
}

// TruncationNotice is appended when content is truncated (host wording).
func TruncationNotice(returned, total int) string {
	return fmt.Sprintf("\n\n---\nShowing %s of %s characters. The omitted tail is not retrievable.",
		strconv.Itoa(returned), strconv.Itoa(total))
}
