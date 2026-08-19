package search

import (
	"context"
	"fmt"
	"strings"
)

// XAI is a hosted xAI web_search source.
type XAI struct {
	Client *ResponsesClient
	Model  string
}

// BuildXAIWebSearchRequest ports host buildXaiWebSearchRequest.
func BuildXAIWebSearchRequest(model, query string, req Request, includeToolChoice bool) (map[string]any, *Error) {
	domains := CleanedStringList(req.AllowedDomains)
	if len(domains) > XAIMaxAllowedDomains {
		return nil, NewError(CodeInvalidInput, fmt.Sprintf(
			"xAI web search accepts at most %d allowed domains; received %d.",
			XAIMaxAllowedDomains, len(domains),
		))
	}
	tool := map[string]any{"type": "web_search"}
	if len(domains) > 0 {
		tool["filters"] = map[string]any{"allowed_domains": domains}
	}
	body := map[string]any{
		"model":        model,
		"input":        MessageInput(query),
		"instructions": WebSearchInstructions,
		"tools":        []any{tool},
		"stream":       false,
		"store":        false,
	}
	if includeToolChoice {
		body["tool_choice"] = map[string]any{"type": "web_search"}
	}
	return body, nil
}

func (x XAI) Search(ctx context.Context, req Request) (Result, error) {
	if x.Client == nil || strings.TrimSpace(x.Model) == "" {
		return Result{}, NewError(CodeMissingConfig, "xai is not configured")
	}
	if err := RejectOfficialXAIBaseURL(x.Client.BaseURL); err != nil {
		return Result{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, SourceTimeout)
	defer cancel()

	body, perr := BuildXAIWebSearchRequest(x.Model, req.Query, req, true)
	if perr != nil {
		return Result{}, perr
	}
	normalized, err := x.Client.Post(ctx, body, req.Limit)
	if err != nil {
		if !IsRetryableToolChoiceError(err) {
			return Result{}, err
		}
		retryBody, rerr := BuildXAIWebSearchRequest(x.Model, req.Query, req, false)
		if rerr != nil {
			return Result{}, rerr
		}
		normalized, err = x.Client.Post(ctx, retryBody, req.Limit)
		if err != nil {
			return Result{}, err
		}
	}
	if !HasWebSearchSuccess(normalized) {
		return Result{}, NewError(CodeInvalidResponse, "web search backend returned no answer or sources")
	}
	return backendToResult(normalized), nil
}
