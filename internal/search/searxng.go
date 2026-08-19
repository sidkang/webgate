package search

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const defaultTimeout = 60 * time.Second

type SearXNG struct {
	BaseURL    string
	HTTPClient *http.Client
	Headers    map[string]string
}

type searxngResponse struct {
	Answers []string `json:"answers"`
	Results []struct {
		Title   string `json:"title"`
		URL     string `json:"url"`
		Content string `json:"content"`
	} `json:"results"`
}

func (s SearXNG) Search(ctx context.Context, query string, limit int) (Result, error) {
	base := strings.TrimRight(s.BaseURL, "/")
	if base == "" {
		return Result{}, fmt.Errorf("searxng base URL is empty")
	}
	u, err := url.Parse(base + "/search")
	if err != nil {
		return Result{}, err
	}
	q := u.Query()
	q.Set("q", query)
	q.Set("format", "json")
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("Accept", "application/json")
	for k, v := range s.Headers {
		req.Header.Set(k, v)
	}

	client := s.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: defaultTimeout}
	}
	resp, err := client.Do(req)
	if err != nil {
		return Result{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Result{}, fmt.Errorf("searxng returned HTTP %d", resp.StatusCode)
	}

	var payload searxngResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return Result{}, fmt.Errorf("searxng returned invalid JSON")
	}

	var sources []Source
	for _, item := range payload.Results {
		if strings.TrimSpace(item.URL) == "" {
			continue
		}
		title := strings.TrimSpace(item.Title)
		if title == "" {
			title = item.URL
		}
		sources = append(sources, Source{
			Title:   title,
			URL:     item.URL,
			Snippet: strings.TrimSpace(item.Content),
		})
		if limit > 0 && len(sources) >= limit {
			break
		}
	}

	var parts []string
	for _, a := range payload.Answers {
		if t := strings.TrimSpace(a); t != "" {
			parts = append(parts, t)
		}
	}
	for _, src := range sources {
		if src.Snippet != "" {
			parts = append(parts, src.Snippet+"\nSource: "+src.Title+" ("+src.URL+")")
			continue
		}
		parts = append(parts, "Source: "+src.Title+" ("+src.URL+")")
	}

	return Result{Answer: strings.Join(parts, "\n\n"), Sources: sources}, nil
}
