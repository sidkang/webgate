package search

import (
	"context"
	"strings"
)

// OpenAI is a hosted OpenAI web_search source.
type OpenAI struct {
	Client *ResponsesClient
	Model  string
}

// BuildOpenAIWebSearchRequest ports host buildOpenAIWebSearchRequest.
func BuildOpenAIWebSearchRequest(model, query string, req Request) map[string]any {
	tool := map[string]any{
		"type":                 "web_search",
		"external_web_access":  true,
		"search_context_size":  "high",
	}
	if size := strings.TrimSpace(req.SearchContextSize); size != "" {
		tool["search_context_size"] = size
	}
	if domains := CleanedStringList(req.AllowedDomains); len(domains) > 0 {
		tool["filters"] = map[string]any{"allowed_domains": domains}
	}
	if loc := CleanedLocation(req.UserLocation); loc != nil {
		tool["user_location"] = loc
	}
	return map[string]any{
		"model":        model,
		"input":        MessageInput(query),
		"instructions": WebSearchInstructions,
		"tools":        []any{tool},
		"tool_choice":  map[string]any{"type": "web_search"},
		"stream":       false,
		"store":        false,
	}
}

func (o OpenAI) Search(ctx context.Context, req Request) (Result, error) {
	if o.Client == nil || strings.TrimSpace(o.Model) == "" {
		return Result{}, NewError(CodeMissingConfig, "openai is not configured")
	}
	ctx, cancel := context.WithTimeout(ctx, SourceTimeout)
	defer cancel()

	body := BuildOpenAIWebSearchRequest(o.Model, req.Query, req)
	normalized, err := o.Client.Post(ctx, body, req.Limit)
	if err != nil {
		return Result{}, err
	}
	if !HasWebSearchSuccess(normalized) {
		return Result{}, NewError(CodeInvalidResponse, "web search backend returned no answer or sources")
	}
	return backendToResult(normalized), nil
}
