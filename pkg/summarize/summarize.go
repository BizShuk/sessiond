// Package summarize defines the contract and implementations that turn a raw
// user/assistant exchange into a one-line session summary.
package summarize

import "context"

// Result is a summarized turn.
type Result struct {
	User    string // cleaned prompt, trimmed to a readable length
	Summary string // one-line summary
	Source  string // heuristic | llm | native
}

// Summarizer produces a Result from a user prompt and assistant reply.
type Summarizer interface {
	Summarize(ctx context.Context, userText, assistantText string) (Result, error)
}
