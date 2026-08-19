package fetch

import (
	"context"
	"strings"

	readability "github.com/go-shiori/go-readability"
	"github.com/markusmobius/go-trafilatura"
)

// DefaultDefuddle extracts main content via trafilatura (Defuddle stand-in).
func DefaultDefuddle(ctx context.Context, html, pageURL string) (string, string, error) {
	if err := ctx.Err(); err != nil {
		return "", "", err
	}
	opts := trafilatura.Options{Focus: trafilatura.FavorRecall}
	if pageURL != "" {
		if u, reason, _ := ParseFetchURL(pageURL); reason == "" && u != nil {
			opts.OriginalURL = u
		}
	}
	res, err := trafilatura.Extract(strings.NewReader(html), opts)
	if err != nil || res == nil {
		return "", "", err
	}
	content := strings.TrimSpace(res.ContentText)
	title := strings.TrimSpace(res.Metadata.Title)
	return title, content, nil
}

// DefaultHTMLExtractor extracts via go-readability.
func DefaultHTMLExtractor(ctx context.Context, html, pageURL string) (string, string, error) {
	if err := ctx.Err(); err != nil {
		return "", "", err
	}
	u, reason, _ := ParseFetchURL(pageURL)
	if reason != "" {
		u = nil
	}
	article, err := readability.FromReader(strings.NewReader(html), u)
	if err != nil {
		return "", "", err
	}
	return strings.TrimSpace(article.Title), strings.TrimSpace(article.TextContent), nil
}
