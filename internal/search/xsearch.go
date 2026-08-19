package search

import (
	"context"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// XSearchRequest is the /v1/x_search payload.
type XSearchRequest struct {
	Query    string
	FromDate string
	ToDate   string
}

// XSearch is the xAI x_search tool source (never falls back to web_search).
type XSearch struct {
	Client *ResponsesClient
	Model  string
}

var dateRE = regexp.MustCompile(`^(\d{4})-(\d{2})-(\d{2})$`)

func NormalizedSearchDate(value, field string) (string, *Error) {
	if value == "" {
		return "", nil
	}
	normalized := strings.TrimSpace(value)
	m := dateRE.FindStringSubmatch(normalized)
	if m == nil {
		return "", NewError(CodeInvalidInput, field+" must be a valid date in YYYY-MM-DD format.")
	}
	year, _ := strconv.Atoi(m[1])
	month, _ := strconv.Atoi(m[2])
	day, _ := strconv.Atoi(m[3])
	leap := year%4 == 0 && (year%100 != 0 || year%400 == 0)
	daysInMonth := []int{31, 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31}
	if leap {
		daysInMonth[1] = 29
	}
	if year < 1 || month < 1 || month > 12 || day < 1 || day > daysInMonth[month-1] {
		return "", NewError(CodeInvalidInput, field+" must be a valid date in YYYY-MM-DD format.")
	}
	return normalized, nil
}

// BuildXSearchRequest ports host buildXSearchRequest.
func BuildXSearchRequest(model string, req XSearchRequest, includeToolChoice bool) (map[string]any, *Error) {
	fromDate, err := NormalizedSearchDate(req.FromDate, "from_date")
	if err != nil {
		return nil, err
	}
	toDate, err := NormalizedSearchDate(req.ToDate, "to_date")
	if err != nil {
		return nil, err
	}
	if fromDate != "" && toDate != "" && fromDate > toDate {
		return nil, NewError(CodeInvalidInput, "from_date must be on or before to_date.")
	}
	tool := map[string]any{"type": "x_search"}
	if fromDate != "" {
		tool["from_date"] = fromDate
	}
	if toDate != "" {
		tool["to_date"] = toDate
	}
	body := map[string]any{
		"model":        model,
		"input":        MessageInput(req.Query),
		"instructions": XSearchInstructions,
		"tools":        []any{tool},
		"stream":       false,
		"store":        false,
	}
	if includeToolChoice {
		body["tool_choice"] = map[string]any{"type": "x_search"}
	}
	return body, nil
}

func isXHost(hostname string) bool {
	h := strings.ToLower(hostname)
	return h == "x.com" || h == "www.x.com" || h == "twitter.com" || h == "www.twitter.com"
}

func hasXSearchCall(resp BackendResponse) bool {
	for _, item := range resp.OutputItems {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if typ, _ := m["type"].(string); typ == "x_search_call" {
			return true
		}
	}
	return false
}

func hasXCitation(resp BackendResponse) bool {
	// Host structuredCitationUrls: citationUrls / citations only — never Sources
	// scraped from answer markdown/bare links.
	check := func(u string) bool {
		parsed, err := url.Parse(u)
		if err != nil {
			return false
		}
		return isXHost(parsed.Hostname())
	}
	for _, u := range resp.CitationURLs {
		if check(u) {
			return true
		}
	}
	return false
}

// IsXSearchSuccess requires trimmed answer and X evidence:
// x_search_call in output items, or a structured citation URL on an X/Twitter host.
func IsXSearchSuccess(resp BackendResponse) bool {
	return strings.TrimSpace(resp.Answer) != "" && (hasXSearchCall(resp) || hasXCitation(resp))
}

func (x XSearch) Search(ctx context.Context, req XSearchRequest) (Result, error) {
	if x.Client == nil || strings.TrimSpace(x.Model) == "" {
		return Result{}, NewError(CodeMissingConfig, "x_search is not configured")
	}
	if err := RejectOfficialXAIBaseURL(x.Client.BaseURL); err != nil {
		return Result{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, SourceTimeout)
	defer cancel()

	body, perr := BuildXSearchRequest(x.Model, req, true)
	if perr != nil {
		return Result{}, perr
	}
	normalized, err := x.Client.Post(ctx, body, 0)
	if err != nil {
		if !IsRetryableToolChoiceError(err) {
			return Result{}, err
		}
		retryBody, rerr := BuildXSearchRequest(x.Model, req, false)
		if rerr != nil {
			return Result{}, rerr
		}
		normalized, err = x.Client.Post(ctx, retryBody, 0)
		if err != nil {
			return Result{}, err
		}
	}
	if !IsXSearchSuccess(normalized) {
		return Result{}, NewError(CodeInvalidResponse, "x search backend returned no X result")
	}
	return backendToResult(normalized), nil
}
