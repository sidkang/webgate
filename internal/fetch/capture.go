package fetch

import "context"

// Page is a captured browser page.
type Page struct {
	RequestedURL string
	URL          string
	Title        string
	HTML         string
}

// Capturer acquires page HTML through CDP (or a test double).
type Capturer interface {
	Capture(ctx context.Context, pageURL string) (Page, error)
}

// CapturerFunc adapts a function to Capturer.
type CapturerFunc func(ctx context.Context, pageURL string) (Page, error)

func (f CapturerFunc) Capture(ctx context.Context, pageURL string) (Page, error) {
	return f(ctx, pageURL)
}

// FixedCapturer always returns the same page or error.
type FixedCapturer struct {
	Page Page
	Err  error
}

func (f FixedCapturer) Capture(context.Context, string) (Page, error) {
	return f.Page, f.Err
}
