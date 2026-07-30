package summarize

import "context"

// Heuristic derives the summary from the user prompt's first line — the same
// cheap signal Grok stores as session_summary. No LLM, no network, no cost.
type Heuristic struct{}

// Summarize returns a zero-cost summary derived from the user prompt.
func (Heuristic) Summarize(
	_ context.Context,
	userText string,
	_ string,
) (Result, error) {
	first := firstLine(userText)
	return Result{
		User:    truncate(userText, 120),
		Summary: truncate(first, 60),
		Source:  "heuristic",
	}, nil
}
