package summarize

import (
	"context"
	"testing"
)

func TestHeuristicSummarizeUsesCleanedPromptAndFirstLine(t *testing.T) {
	result, err := (Heuristic{}).Summarize(
		context.Background(),
		"  first line  \nsecond line  ",
		"ignored assistant reply",
	)

	if err != nil {
		t.Fatal(err)
	}
	if result.User != "first line  \nsecond line" {
		t.Fatalf("user = %q, want cleaned prompt", result.User)
	}
	if result.Summary != "first line" {
		t.Fatalf("summary = %q, want first line", result.Summary)
	}
	if result.Source != "heuristic" {
		t.Fatalf("source = %q, want heuristic", result.Source)
	}
}
