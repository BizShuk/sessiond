package summarize

import "testing"

// TestStripThought covers the gemma "<thought>...</thought>" scratchpad
// leakage reported against real ingest output: nearly every LLM summary was
// starting with raw reasoning text instead of the final sentence, because
// the model was cut off mid-thought before MaxTokens=96 ran out.
func TestStripThought(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "no thought tag passes through unchanged",
			in:   "使用者新增了 git hooks 安裝流程,已完成。",
			want: "使用者新增了 git hooks 安裝流程,已完成。",
		},
		{
			name: "closed thought block is dropped, trailing summary kept",
			in:   "<thought>* Role: AI coding session summarizer.</thought>使用者要求修 bug,已修好。",
			want: "使用者要求修 bug,已修好。",
		},
		{
			name: "closed thought block is case-insensitive",
			in:   "<THOUGHT>reasoning...</THOUGHT>結果如預期。",
			want: "結果如預期。",
		},
		{
			name: "unclosed thought block (truncated mid-reasoning) yields empty",
			in:   `<thought>*   Input: A single turn (User: "the hook input...`,
			want: "",
		},
		{
			name: "empty input stays empty",
			in:   "",
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := stripThought(tc.in); got != tc.want {
				t.Errorf("stripThought(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
