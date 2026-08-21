package search

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/packages/param"
	"github.com/openai/openai-go/v3/responses"
)

const (
	SourceTimeout         = 60 * time.Second
	maxRawResponseBytes   = 1024 * 1024
	maxBackendErrChars    = 1000
	WebSearchInstructions = "Search the web and answer only from the retrieved results. Cite sources."
	XSearchInstructions   = "Search X and answer only from retrieved X posts. Cite X URLs."
	XAIMaxAllowedDomains  = 5
)

// BackendResponse is a normalized Responses API payload.
type BackendResponse struct {
	Answer       string
	Sources      []Source
	CitationURLs []string
	OutputItems  []any
}

// ResponsesClient POSTs to an OpenAI-compatible /responses endpoint via openai-go.
type ResponsesClient struct {
	BaseURL    string
	APIKey     string
	HTTPClient *http.Client
}

// ResponsesBaseURL returns the SDK root (origin or …/v1), never ending in /responses.
func ResponsesBaseURL(baseURL string) string {
	normalized := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if strings.HasSuffix(normalized, "/responses") {
		return strings.TrimSuffix(normalized, "/responses")
	}
	return normalized
}

// ResponsesURL is the full /responses endpoint (diagnostics / legacy).
func ResponsesURL(baseURL string) string {
	base := ResponsesBaseURL(baseURL)
	if base == "" {
		return "/responses"
	}
	return base + "/responses"
}

// IsOfficialXAIHostname reports official xAI hosts that must be rejected.
func IsOfficialXAIHostname(hostname string) bool {
	normalized := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(hostname), "."))
	return normalized == "api.x.ai" || strings.HasSuffix(normalized, ".api.x.ai")
}

// RejectOfficialXAIBaseURL returns missing_config when baseURL points at official xAI.
func RejectOfficialXAIBaseURL(baseURL string) *Error {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || u.Hostname() == "" {
		return nil
	}
	if IsOfficialXAIHostname(u.Hostname()) {
		return NewError(CodeMissingConfig, "xAI search only supports a CLIProxyAPI route. Direct official xAI endpoints such as api.x.ai are rejected.")
	}
	return nil
}

func CleanedStringList(values []string) []string {
	var out []string
	for _, v := range values {
		if t := strings.TrimSpace(v); t != "" {
			out = append(out, t)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// CleanedLocation returns approximate location fields; country only if /^[A-Za-z]{2}$/.
func CleanedLocation(loc *UserLocation) map[string]string {
	if loc == nil {
		return nil
	}
	result := map[string]string{}
	country := strings.TrimSpace(loc.Country)
	if country != "" && regexp.MustCompile(`(?i)^[a-z]{2}$`).MatchString(country) {
		result["country"] = strings.ToUpper(country)
	}
	for _, pair := range []struct{ key, val string }{
		{"region", loc.Region},
		{"city", loc.City},
		{"timezone", loc.Timezone},
	} {
		if t := strings.TrimSpace(pair.val); t != "" {
			result[pair.key] = t
		}
	}
	if len(result) == 0 {
		return nil
	}
	result["type"] = "approximate"
	return result
}

func MessageInput(query string) []map[string]any {
	return []map[string]any{{
		"type": "message",
		"role": "user",
		"content": []map[string]any{{
			"type": "input_text",
			"text": query,
		}},
	}}
}

func HasWebSearchSuccess(r BackendResponse) bool {
	if strings.TrimSpace(r.Answer) == "" {
		return false
	}
	if hasWebSearchCall(r) {
		return true
	}
	for _, u := range r.CitationURLs {
		if strings.TrimSpace(u) != "" {
			return true
		}
	}
	return false
}

func hasWebSearchCall(resp BackendResponse) bool {
	for _, item := range resp.OutputItems {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if typ, _ := m["type"].(string); typ == "web_search_call" {
			return true
		}
	}
	return false
}

func IsRetryableToolChoiceError(err error) bool {
	var se *Error
	if !asSearchError(err, &se) || se == nil {
		return false
	}
	if se.Code == CodeUnsupportedToolChoice {
		return true
	}
	// Status is encoded in Message as "HTTP NNN: ..." for Responses errors.
	status := httpStatusFromMessage(se.Message)
	if status != 400 && status != 422 {
		return false
	}
	return toolChoiceShapeFailure(se.Message)
}

func asSearchError(err error, target **Error) bool {
	if err == nil {
		return false
	}
	e, ok := err.(*Error)
	if !ok {
		return false
	}
	*target = e
	return true
}

func httpStatusFromMessage(msg string) int {
	var status int
	_, _ = fmt.Sscanf(msg, "HTTP %d:", &status)
	return status
}

func toolChoiceShapeFailure(text string) bool {
	return regexp.MustCompile(`(?i)tool[_\s-]?choice|modeltoolchoice`).MatchString(text)
}

func unsupportedToolWording(text string) bool {
	return regexp.MustCompile(`(?i)unsupported[_\s-]*tool|unknown tool|tool[^\n]{0,80}(not supported|unsupported)`).MatchString(text) ||
		regexp.MustCompile(`(?i)(?:web_search|x_search)[^\n]{0,80}(not supported|unsupported)`).MatchString(text)
}

func unsupportedModelWording(text string) bool {
	return regexp.MustCompile(`(?i)unsupported[_\s-]*model|unknown model|model[^\n]{0,80}(not supported|unsupported|not found|does not exist)`).MatchString(text)
}

func errorCodeForHTTPStatus(status int, body string) Code {
	text := strings.ToLower(body)
	switch status {
	case 401, 403:
		return CodeAuthFailed
	case 408:
		return CodeTimeout
	case 429:
		return CodeRateLimited
	case 400, 422:
		if toolChoiceShapeFailure(text) {
			return CodeUnsupportedToolChoice
		}
		if unsupportedToolWording(text) {
			return CodeUnsupportedTool
		}
		if unsupportedModelWording(text) {
			return CodeUnsupportedModel
		}
		return CodeInvalidInput
	case 404:
		if strings.Contains(text, "model") {
			return CodeUnsupportedModel
		}
	}
	return CodeBackendError
}

func (c *ResponsesClient) client() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return &http.Client{Timeout: SourceTimeout}
}

func (c *ResponsesClient) sdkClient() openai.Client {
	opts := []option.RequestOption{
		option.WithBaseURL(ResponsesBaseURL(c.BaseURL)),
		option.WithHTTPClient(c.client()),
		option.WithMaxRetries(0),
	}
	if c.APIKey != "" {
		opts = append(opts, option.WithAPIKey(c.APIKey))
	}
	return openai.NewClient(opts...)
}

// Post sends a Responses body (host-faithful JSON) via openai-go and normalizes the result.
func (c *ResponsesClient) Post(ctx context.Context, body map[string]any, limit int) (BackendResponse, error) {
	if err := RejectOfficialXAIBaseURL(c.BaseURL); err != nil {
		return BackendResponse{}, err
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return BackendResponse{}, NewError(CodeBackendError, "failed to encode request")
	}

	cli := c.sdkClient()
	resp, err := cli.Responses.New(ctx, param.Override[responses.ResponseNewParams](json.RawMessage(payload)))
	if err != nil {
		return BackendResponse{}, mapSDKError(err, c.APIKey)
	}
	if resp == nil {
		return BackendResponse{}, NewError(CodeInvalidResponse, "web search backend returned empty response")
	}
	raw := resp.RawJSON()
	if len(raw) > maxRawResponseBytes {
		return BackendResponse{}, NewError(CodeInvalidResponse, "responses body too large")
	}
	var decoded any
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		return BackendResponse{}, NewError(CodeInvalidResponse, "web search backend returned non-JSON response")
	}
	return NormalizeBackendResponse(decoded, limit), nil
}

func mapSDKError(err error, apiKey string) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}

	var apiErr *openai.Error
	if errors.As(err, &apiErr) && apiErr != nil {
		msg := strings.TrimSpace(apiErr.Message)
		if msg == "" {
			msg = strings.TrimSpace(apiErr.RawJSON())
		}
		if msg == "" {
			msg = apiErr.Error()
		}
		// Prefer nested error.message when RawJSON is the full envelope.
		var parsed map[string]any
		if json.Unmarshal([]byte(apiErr.RawJSON()), &parsed) == nil {
			if errObj, ok := parsed["error"].(map[string]any); ok {
				if m, ok := errObj["message"].(string); ok && strings.TrimSpace(m) != "" {
					msg = m
				}
			} else if m, ok := parsed["message"].(string); ok && strings.TrimSpace(m) != "" {
				msg = m
			}
		}
		msg = redactSecrets(msg, apiKey)
		if len(msg) > maxBackendErrChars {
			msg = msg[:maxBackendErrChars]
		}
		code := errorCodeForHTTPStatus(apiErr.StatusCode, msg)
		return &Error{Code: code, Message: fmt.Sprintf("HTTP %d: %s", apiErr.StatusCode, msg)}
	}

	classified := Classify(err)
	if classified.Code == CodeTimeout || classified.Code == CodeAborted {
		return err
	}
	return NewError(CodeBackendError, "responses request failed")
}

func redactSecrets(text, apiKey string) string {
	if apiKey == "" {
		return text
	}
	out := strings.ReplaceAll(text, apiKey, "[redacted]")
	out = regexp.MustCompile(`(?i)(Bearer)\s+\S+`).ReplaceAllString(out, "$1 [redacted]")
	return out
}

// NormalizeBackendResponse walks a Responses JSON payload into answer + sources.
func NormalizeBackendResponse(raw any, limit int) BackendResponse {
	texts := []string{}
	sources := map[string]Source{}
	citationURLs := []string{}
	var outputItems []any
	seenText := map[string]struct{}{}

	var walk func(any)
	walk = func(v any) {
		switch t := v.(type) {
		case []any:
			for _, item := range t {
				walk(item)
			}
		case map[string]any:
			if s, ok := t["output_text"].(string); ok && strings.TrimSpace(s) != "" {
				texts = append(texts, s)
			}
			if typ, _ := t["type"].(string); typ == "output_text" {
				if s, ok := t["text"].(string); ok && strings.TrimSpace(s) != "" {
					texts = append(texts, s)
				}
				if anns, ok := t["annotations"].([]any); ok {
					for _, a := range anns {
						collectAnnotation(a, sources, &citationURLs)
					}
				}
			}
			if typ, _ := t["type"].(string); typ == "url_citation" {
				collectAnnotation(t, sources, &citationURLs)
			}
			if arr, ok := t["sources"].([]any); ok {
				for _, s := range arr {
					collectSourceNode(s, sources)
				}
			}
			if arr, ok := t["citations"].([]any); ok {
				for _, c := range arr {
					collectCitationNode(c, sources, &citationURLs)
				}
			}
			if arr, ok := t["output"].([]any); ok {
				for _, item := range arr {
					outputItems = append(outputItems, item)
				}
			}
			for _, child := range t {
				walk(child)
			}
		}
	}
	walk(raw)

	var answerParts []string
	for _, t := range texts {
		trimmed := strings.TrimSpace(t)
		if trimmed == "" {
			continue
		}
		if _, ok := seenText[trimmed]; ok {
			continue
		}
		seenText[trimmed] = struct{}{}
		answerParts = append(answerParts, trimmed)
	}
	answer := strings.Join(answerParts, "\n")
	// Do not scrape markdown/bare links from answer text into Sources.
	// Evidence is structured annotations / sources / citations only.

	list := make([]Source, 0, len(sources))
	for _, s := range sources {
		list = append(list, s)
	}
	if limit > 0 && len(list) > limit {
		list = list[:limit]
	}
	if limit > 0 && len(citationURLs) > limit {
		citationURLs = citationURLs[:limit]
	}
	return BackendResponse{
		Answer:       answer,
		Sources:      list,
		CitationURLs: citationURLs,
		OutputItems:  outputItems,
	}
}

func collectSourceNode(v any, sources map[string]Source) {
	m, ok := v.(map[string]any)
	if !ok {
		return
	}
	u, _ := m["url"].(string)
	u = strings.TrimSpace(u)
	if u == "" {
		return
	}
	title, _ := m["title"].(string)
	if title == "" {
		title, _ = m["name"].(string)
	}
	if strings.TrimSpace(title) == "" {
		title = u
	}
	snippet, _ := m["snippet"].(string)
	if snippet == "" {
		snippet, _ = m["description"].(string)
	}
	key := normalizeURLKey(u)
	if existing, ok := sources[key]; ok {
		if existing.Snippet == "" && strings.TrimSpace(snippet) != "" {
			existing.Snippet = strings.TrimSpace(snippet)
			sources[key] = existing
		}
		return
	}
	sources[key] = Source{Title: strings.TrimSpace(title), URL: u, Snippet: strings.TrimSpace(snippet)}
}

func collectCitationNode(v any, sources map[string]Source, citationURLs *[]string) {
	if s, ok := v.(string); ok {
		u := strings.TrimSpace(s)
		if u == "" {
			return
		}
		parsed, err := url.Parse(u)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			return
		}
		*citationURLs = append(*citationURLs, u)
		key := normalizeURLKey(u)
		if _, ok := sources[key]; !ok {
			sources[key] = Source{Title: parsed.Hostname(), URL: u}
		}
		return
	}
	m, ok := v.(map[string]any)
	if !ok {
		return
	}
	u, _ := m["url"].(string)
	u = strings.TrimSpace(u)
	if u == "" {
		return
	}
	*citationURLs = append(*citationURLs, u)
	title, _ := m["title"].(string)
	if title == "" {
		title, _ = m["name"].(string)
	}
	if strings.TrimSpace(title) == "" {
		title = u
	}
	cited, _ := m["citedText"].(string)
	if cited == "" {
		cited, _ = m["cited_text"].(string)
	}
	if cited == "" {
		cited, _ = m["text"].(string)
	}
	key := normalizeURLKey(u)
	sources[key] = Source{Title: strings.TrimSpace(title), URL: u, Snippet: strings.TrimSpace(cited)}
}

func collectAnnotation(v any, sources map[string]Source, citationURLs *[]string) {
	m, ok := v.(map[string]any)
	if !ok {
		return
	}
	if typ, _ := m["type"].(string); typ == "url_citation" {
		collectCitationNode(m, sources, citationURLs)
		return
	}
	if nested, ok := m["url_citation"]; ok {
		collectCitationNode(nested, sources, citationURLs)
	}
}

func collectTextLinks(text string, sources map[string]Source) {
	md := regexp.MustCompile(`\[([^\]\n]{1,200})\]\((https?://[^)\s]+)\)`)
	for _, m := range md.FindAllStringSubmatch(text, -1) {
		title, u := m[1], m[2]
		key := normalizeURLKey(u)
		if _, ok := sources[key]; !ok {
			sources[key] = Source{Title: strings.TrimSpace(title), URL: u}
		}
	}
	bare := regexp.MustCompile(`https?://[^\s<>)\]]+`)
	for _, u := range bare.FindAllString(text, -1) {
		u = strings.TrimRight(u, ".,;:!?")
		key := normalizeURLKey(u)
		if _, ok := sources[key]; !ok {
			host := u
			if parsed, err := url.Parse(u); err == nil && parsed.Hostname() != "" {
				host = parsed.Hostname()
			}
			sources[key] = Source{Title: host, URL: u}
		}
	}
}

func normalizeURLKey(u string) string {
	parsed, err := url.Parse(strings.TrimSpace(u))
	if err != nil {
		return strings.TrimSpace(u)
	}
	parsed.Fragment = ""
	return parsed.String()
}

func backendToResult(b BackendResponse) Result {
	return Result{Answer: b.Answer, Sources: b.Sources}
}
