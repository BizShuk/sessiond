// Package summarize turns a raw user/assistant exchange into a one-line summary.
// Phase 1 ships only the zero-cost Heuristic. The Summarizer interface is the
// seam where the gemma (agentSDK provider/google) backend plugs in later; when
// it fails or is disabled, callers fall back to Heuristic so ingest never stalls.
package summarize

import "strings"

// Result is a summarized turn.
type Result struct {
	User    string // cleaned prompt, trimmed to a readable length
	Summary string // one-line summary
	Source  string // heuristic | llm | native
}

// Summarizer produces a Result from a user prompt and assistant reply.
type Summarizer interface {
	Summarize(userText, assistantText string) Result
}

// Heuristic derives the summary from the user prompt's first line — the same
// cheap signal Grok stores as session_summary. No LLM, no network, no cost.
type Heuristic struct{}

func (Heuristic) Summarize(userText, assistantText string) Result {
	first := firstLine(userText)
	return Result{
		User:    truncate(userText, 120),
		Summary: truncate(first, 60),
		Source:  "heuristic",
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

func truncate(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= n {
		return string(r)
	}
	return strings.TrimSpace(string(r[:n])) + "…"
}
