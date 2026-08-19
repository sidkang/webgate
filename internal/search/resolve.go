package search

import "strings"

// Known source names and provider aliases.
const (
	SourceSearXNG = "searxng"
	SourceOpenAI  = "openai"
	SourceXAI     = "xai"

	ProviderLLM  = "llm"
	ProviderAuto = "auto"
)

var knownSources = map[string]struct{}{
	SourceSearXNG: {},
	SourceOpenAI:  {},
	SourceXAI:     {},
}

// ProviderPlan is the resolved source chain for a request.
type ProviderPlan struct {
	Chain       []string
	SkipMissing bool
	Parallel    bool // true for provider JSON arrays (list-merge)
}

// ParseProvider turns a JSON provider value into a sequential or parallel plan.
// Omitted / null / empty / "llm" → openai then xai (skip unconfigured).
// "auto" → searxng then openai then xai (skip unconfigured).
// A single known name → that source only (no skip; missing_config if unset).
// JSON array of ≥2 known names → parallel list-merge (skip unconfigured).
// "google", "all", unknown names, length-1 arrays, and non-strings are invalid_input.
func ParseProvider(v any) (ProviderPlan, *Error) {
	if v == nil {
		return llmPlan(), nil
	}
	switch t := v.(type) {
	case string:
		name := strings.TrimSpace(t)
		switch name {
		case "", ProviderLLM:
			return llmPlan(), nil
		case ProviderAuto:
			return ProviderPlan{
				Chain:       []string{SourceSearXNG, SourceOpenAI, SourceXAI},
				SkipMissing: true,
			}, nil
		case "google", "all":
			return ProviderPlan{}, NewError(CodeInvalidInput, "provider is invalid")
		default:
			if _, ok := knownSources[name]; !ok {
				return ProviderPlan{}, NewError(CodeInvalidInput, "provider is invalid")
			}
			return ProviderPlan{
				Chain:       []string{name},
				SkipMissing: false,
			}, nil
		}
	case []any:
		return parseProviderList(t)
	case []string:
		items := make([]any, len(t))
		for i, s := range t {
			items[i] = s
		}
		return parseProviderList(items)
	default:
		return ProviderPlan{}, NewError(CodeInvalidInput, "provider must be a string or array")
	}
}

func parseProviderList(items []any) (ProviderPlan, *Error) {
	if len(items) < 2 {
		return ProviderPlan{}, NewError(CodeInvalidInput, "provider array must contain at least two sources")
	}
	seen := map[string]struct{}{}
	var names []string
	for _, item := range items {
		s, ok := item.(string)
		if !ok {
			return ProviderPlan{}, NewError(CodeInvalidInput, "provider array entries must be strings")
		}
		name := strings.TrimSpace(s)
		if name == "google" || name == "all" {
			return ProviderPlan{}, NewError(CodeInvalidInput, "provider is invalid")
		}
		if _, ok := knownSources[name]; !ok {
			return ProviderPlan{}, NewError(CodeInvalidInput, "provider is invalid")
		}
		if _, dup := seen[name]; dup {
			continue
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}
	if len(names) < 2 {
		return ProviderPlan{}, NewError(CodeInvalidInput, "provider array must contain at least two sources")
	}
	return ProviderPlan{
		Chain:       names,
		SkipMissing: true,
		Parallel:    true,
	}, nil
}

func llmPlan() ProviderPlan {
	return ProviderPlan{
		Chain:       []string{SourceOpenAI, SourceXAI},
		SkipMissing: true,
	}
}
