package search

import (
	"context"
	"regexp"
	"strings"
	"sync"
)

const (
	MergeInstructions = "Merge the provided web search results into one concise answer. Use only the provided answers and listed sources. Do not invent URLs. Return only the merged answer text."

	maxPromptAnswerChars        = 4000
	maxPromptQueryChars         = 2000
	maxPromptSourceSectionChars = 6000
	maxPromptSourceFieldChars   = 1000
	maxPromptDiagnosticsChars   = 500
	maxPromptChars              = 16_000
	maxMergeDiagnosticChars     = 1000
)

// LabelledAnswer is one successful source result for merging.
type LabelledAnswer struct {
	Source  string
	Answer  string
	Sources []Source
}

// Merger synthesizes one answer from labelled per-source results.
type Merger interface {
	Merge(ctx context.Context, query string, labelled []LabelledAnswer) (string, error)
}

// MergerFunc adapts a function to Merger.
type MergerFunc func(ctx context.Context, query string, labelled []LabelledAnswer) (string, error)

func (f MergerFunc) Merge(ctx context.Context, query string, labelled []LabelledAnswer) (string, error) {
	return f(ctx, query, labelled)
}

// MergeStatus is returned on list-provider responses when a merge was attempted.
type MergeStatus struct {
	OK    bool   `json:"ok"`
	Code  string `json:"code,omitempty"`
	Error string `json:"error,omitempty"`
}

// ParallelOutcome is the result of a provider-list search.
type ParallelOutcome struct {
	Result   Result
	Provider any // string (single) or []string (list mode)
	Merge    *MergeStatus
}

// MergeAnswers builds the labelled raw answer (host mergeAnswers).
func MergeAnswers(successes []LabelledAnswer) string {
	parts := make([]string, 0, len(successes))
	for _, item := range successes {
		answer := strings.TrimSpace(item.Answer)
		if answer == "" {
			answer = "(No answer text returned.)"
		}
		parts = append(parts, "## "+item.Source+"\n"+answer)
	}
	return strings.Join(parts, "\n\n")
}

// BuildMergePrompt ports host buildMergePrompt (char caps included).
func BuildMergePrompt(query string, successes []LabelledAnswer) string {
	sections := []string{
		"Merge the following web search results into one concise answer.",
		"Use only the provided answers and listed sources. Do not invent URLs.",
		"",
		"Query:\n" + trimChars(strings.TrimSpace(query), maxPromptQueryChars),
	}
	for _, item := range successes {
		answer := strings.TrimSpace(item.Answer)
		if answer == "" {
			answer = "(no answer text)"
		} else {
			answer = trimChars(answer, maxPromptAnswerChars)
		}
		var sourceLines []string
		if len(item.Sources) == 0 {
			sourceLines = []string{"- (none)"}
		} else {
			for _, src := range item.Sources {
				sourceLines = append(sourceLines, "- "+trimChars(src.Title, maxPromptSourceFieldChars)+": "+trimChars(src.URL, maxPromptSourceFieldChars))
			}
		}
		section := strings.Join([]string{
			"### " + item.Source,
			"Answer:\n" + answer,
			"Sources:\n" + strings.Join(sourceLines, "\n"),
		}, "\n")
		sections = append(sections, "", trimChars(section, maxPromptSourceSectionChars))
	}
	prompt := strings.Join(sections, "\n")
	return trimChars(prompt, maxPromptChars)
}

func trimChars(s string, max int) string {
	if max <= 0 || len([]rune(s)) <= max {
		return s
	}
	return string([]rune(s)[:max])
}

// DedupeSources merges evidence by normalized URL; first-seen wins.
func DedupeSources(successes []LabelledAnswer, limit int) []Source {
	seen := map[string]Source{}
	var order []string
	for _, item := range successes {
		for _, src := range item.Sources {
			u := strings.TrimSpace(src.URL)
			if u == "" {
				continue
			}
			key := normalizeURLKey(u)
			if existing, ok := seen[key]; ok {
				if (existing.Title == "" || existing.Title == existing.URL) && src.Title != "" {
					existing.Title = src.Title
				}
				if existing.Snippet == "" && src.Snippet != "" {
					existing.Snippet = src.Snippet
				}
				seen[key] = existing
				continue
			}
			seen[key] = Source{Title: src.Title, URL: u, Snippet: src.Snippet}
			order = append(order, key)
		}
	}
	out := make([]Source, 0, len(order))
	for _, key := range order {
		out = append(out, seen[key])
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

var httpURLInText = regexp.MustCompile(`(?i)https?://[^\s<>"'` + "`" + `]+`)

// UnsupportedAnswerURLs returns http(s) URLs in answer that are not in evidence.
func UnsupportedAnswerURLs(answer string, successes []LabelledAnswer) []string {
	allowed := map[string]struct{}{}
	for _, item := range successes {
		for _, src := range item.Sources {
			if u := strings.TrimSpace(src.URL); u != "" {
				allowed[normalizeURLKey(u)] = struct{}{}
			}
		}
	}
	found := httpURLInText.FindAllString(answer, -1)
	seen := map[string]struct{}{}
	var bad []string
	for _, raw := range found {
		u := strings.TrimRight(raw, "),.;:!?]}")
		if u == "" {
			continue
		}
		key := normalizeURLKey(u)
		if _, ok := allowed[key]; ok {
			continue
		}
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		bad = append(bad, u)
	}
	return bad
}

func boundDiagnostic(msg string) string {
	msg = strings.TrimSpace(msg)
	if len(msg) > maxMergeDiagnosticChars {
		return msg[:maxMergeDiagnosticChars]
	}
	return msg
}

// SearchParallel runs configured sources concurrently and optionally LLM-merges.
func SearchParallel(ctx context.Context, sources map[string]Searcher, names []string, req Request, merger Merger) (ParallelOutcome, *Error) {
	var participants []string
	for _, name := range names {
		if sources[name] != nil {
			participants = append(participants, name)
		}
	}
	if len(participants) == 0 {
		return ParallelOutcome{}, NewError(CodeMissingConfig, "no search source is configured")
	}

	if len(participants) == 1 {
		name := participants[0]
		result, err := sources[name].Search(ctx, req)
		if err != nil {
			classified := Classify(err)
			return ParallelOutcome{}, NewError(classified.Code, classified.Message)
		}
		if IsEmpty(result) {
			return ParallelOutcome{}, NewError(CodeInvalidResponse, "search returned no answer or sources")
		}
		return ParallelOutcome{Result: result, Provider: name}, nil
	}

	type settled struct {
		name   string
		result Result
		err    error
	}
	ch := make(chan settled, len(participants))
	var wg sync.WaitGroup
	for _, name := range participants {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			if err := ctx.Err(); err != nil {
				ch <- settled{name: name, err: err}
				return
			}
			r, err := sources[name].Search(ctx, req)
			ch <- settled{name: name, result: r, err: err}
		}(name)
	}
	go func() {
		wg.Wait()
		close(ch)
	}()

	byName := map[string]settled{}
	for item := range ch {
		byName[item.name] = item
	}

	if err := ctx.Err(); err != nil {
		classified := Classify(err)
		if classified.Code == CodeAborted {
			return ParallelOutcome{}, NewError(CodeAborted, classified.Message)
		}
	}

	var successes []LabelledAnswer
	var last *Error
	for _, name := range participants {
		item := byName[name]
		if item.err != nil {
			classified := Classify(item.err)
			if classified.Code == CodeAborted {
				return ParallelOutcome{}, NewError(CodeAborted, classified.Message)
			}
			last = NewError(classified.Code, classified.Message)
			continue
		}
		if IsEmpty(item.result) {
			last = NewError(CodeInvalidResponse, "search returned no answer or sources")
			continue
		}
		successes = append(successes, LabelledAnswer{
			Source:  name,
			Answer:  item.result.Answer,
			Sources: item.result.Sources,
		})
	}

	if len(successes) == 0 {
		if last == nil {
			last = NewError(CodeBackendError, "all search sources failed")
		}
		return ParallelOutcome{}, last
	}
	if len(successes) == 1 {
		return ParallelOutcome{
			Result:   Result{Answer: successes[0].Answer, Sources: successes[0].Sources},
			Provider: successes[0].Source,
		}, nil
	}

	raw := MergeAnswers(successes)
	evidence := DedupeSources(successes, req.Limit)
	listProvider := append([]string(nil), participants...)

	degraded := func(code Code, message string) ParallelOutcome {
		return ParallelOutcome{
			Result:   Result{Answer: raw, Sources: evidence},
			Provider: listProvider,
			Merge: &MergeStatus{
				OK:    false,
				Code:  string(code),
				Error: boundDiagnostic(message),
			},
		}
	}

	if merger == nil {
		return degraded(CodeMissingConfig, "No merge model is configured."), nil
	}

	merged, err := merger.Merge(ctx, req.Query, successes)
	if err != nil {
		classified := Classify(err)
		if classified.Code == CodeAborted {
			return ParallelOutcome{}, NewError(CodeAborted, classified.Message)
		}
		msg := classified.Message
		if msg == "" {
			msg = string(classified.Code)
		}
		return degraded(classified.Code, msg), nil
	}
	merged = strings.TrimSpace(merged)
	if merged == "" {
		return degraded(CodeInvalidResponse, "merge model returned empty answer"), nil
	}
	if len(UnsupportedAnswerURLs(merged, successes)) > 0 {
		return degraded(CodeInvalidResponse, "merge model returned URLs not present in search evidence"), nil
	}

	return ParallelOutcome{
		Result:   Result{Answer: merged, Sources: evidence},
		Provider: listProvider,
		Merge:    &MergeStatus{OK: true},
	}, nil
}

// OpenAIMerger merges via OpenAI-compatible Responses completion (no tools).
type OpenAIMerger struct {
	Client *ResponsesClient
	Model  string
}

func (m OpenAIMerger) Merge(ctx context.Context, query string, labelled []LabelledAnswer) (string, error) {
	if m.Client == nil || strings.TrimSpace(m.Model) == "" {
		return "", NewError(CodeMissingConfig, "No merge model is configured.")
	}
	ctx, cancel := context.WithTimeout(ctx, SourceTimeout)
	defer cancel()

	body := map[string]any{
		"model":        m.Model,
		"instructions": MergeInstructions,
		"input":        MessageInput(BuildMergePrompt(query, labelled)),
		"stream":       false,
		"store":        false,
	}
	resp, err := m.Client.Post(ctx, body, 0)
	if err != nil {
		return "", err
	}
	text := strings.TrimSpace(resp.Answer)
	if text == "" {
		return "", NewError(CodeInvalidResponse, "merge model returned empty answer")
	}
	if len(UnsupportedAnswerURLs(text, labelled)) > 0 {
		return "", NewError(CodeInvalidResponse, "merge model returned URLs not present in search evidence")
	}
	return text, nil
}
