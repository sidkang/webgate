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
}

// ParseProvider turns a JSON provider value into a sequential plan.
// Omitted / null / empty / "llm" → openai then xai (skip unconfigured).
// "auto" → searxng then openai then xai (skip unconfigured).
// A single known name → that source only (no skip; missing_config if unset).
// "google", "all", unknown names, arrays, and non-strings are invalid_input.
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
	default:
		// Arrays, numbers, objects, bools — not this ticket's list merge.
		return ProviderPlan{}, NewError(CodeInvalidInput, "provider must be a string")
	}
}

func llmPlan() ProviderPlan {
	return ProviderPlan{
		Chain:       []string{SourceOpenAI, SourceXAI},
		SkipMissing: true,
	}
}
