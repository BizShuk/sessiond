package ingest

import (
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestParseClaudeTurns_basic(t *testing.T) {
	// two real prompts, a tool_result user line (skipped), sidechain (skipped).
	jsonl := `{"type":"user","cwd":"/ws/proj","timestamp":"t1","message":{"role":"user","content":"first question"}}
{"type":"assistant","timestamp":"t1","message":{"role":"assistant","content":[{"type":"text","text":"answer one"}]}}
{"type":"user","timestamp":"t2","message":{"role":"user","content":[{"type":"tool_result","content":"x"}]}}
{"type":"assistant","timestamp":"t2","message":{"role":"assistant","content":[{"type":"tool_use","name":"Bash"}]}}
{"type":"user","isSidechain":true,"message":{"role":"user","content":"sub-agent noise"}}
{"type":"user","timestamp":"t3","message":{"role":"user","content":"second question"}}
{"type":"assistant","timestamp":"t3","message":{"role":"assistant","content":[{"type":"text","text":"answer two"}]}}
`
	turns, cwd, err := ParseClaudeTurns(write(t, "c.jsonl", jsonl))
	if err != nil {
		t.Fatal(err)
	}
	if cwd != "/ws/proj" {
		t.Errorf("cwd = %q, want /ws/proj", cwd)
	}
	if len(turns) != 2 {
		t.Fatalf("got %d turns, want 2: %+v", len(turns), turns)
	}
	if turns[0].UserText != "first question" || turns[0].AssistantText != "answer one" {
		t.Errorf("turn0 = %+v", turns[0])
	}
	if turns[1].UserText != "second question" || turns[1].AssistantText != "answer two" {
		t.Errorf("turn1 = %+v", turns[1])
	}
}

func TestParseClaudeTurns_prefersFinalAssistantText(t *testing.T) {
	// One turn: narration before a tool call, then the real answer. Only the
	// end_turn text should survive.
	jsonl := `{"type":"user","timestamp":"t1","message":{"role":"user","content":"do it"}}
{"type":"assistant","timestamp":"t1","message":{"role":"assistant","stop_reason":"tool_use","content":[{"type":"text","text":"let me check first"}]}}
{"type":"assistant","timestamp":"t1","message":{"role":"assistant","stop_reason":"tool_use","content":[{"type":"tool_use","name":"Bash"}]}}
{"type":"assistant","timestamp":"t1","message":{"role":"assistant","stop_reason":"end_turn","content":[{"type":"text","text":"the answer"}]}}
`
	turns, _, err := ParseClaudeTurns(write(t, "final.jsonl", jsonl))
	if err != nil {
		t.Fatal(err)
	}
	if len(turns) != 1 {
		t.Fatalf("got %d turns, want 1", len(turns))
	}
	if turns[0].AssistantText != "the answer" {
		t.Errorf("AssistantText = %q, want %q", turns[0].AssistantText, "the answer")
	}
}

func TestParseClaudeTurns_stopSequenceCountsAsFinal(t *testing.T) {
	jsonl := `{"type":"user","message":{"role":"user","content":"q"}}
{"type":"assistant","message":{"role":"assistant","stop_reason":"stop_sequence","content":[{"type":"text","text":"final via stop sequence"}]}}
`
	turns, _, err := ParseClaudeTurns(write(t, "seq.jsonl", jsonl))
	if err != nil {
		t.Fatal(err)
	}
	if len(turns) != 1 || turns[0].AssistantText != "final via stop sequence" {
		t.Fatalf("got %+v", turns)
	}
}

func TestParseClaudeTurns_dropsIntermediateTextWhenNoFinalText(t *testing.T) {
	// Interrupted turn: every assistant record is mid-tool, so there is no final
	// response content to send to the summarizer.
	jsonl := `{"type":"user","message":{"role":"user","content":"q"}}
{"type":"assistant","message":{"role":"assistant","stop_reason":"tool_use","content":[{"type":"text","text":"partial work"}]}}
`
	turns, _, err := ParseClaudeTurns(write(t, "interrupted.jsonl", jsonl))
	if err != nil {
		t.Fatal(err)
	}
	if len(turns) != 1 || turns[0].AssistantText != "" {
		t.Fatalf("got %+v, want intermediate text omitted", turns)
	}
}

func TestParseClaudeTurns_missingStopReasonTreatedAsFinal(t *testing.T) {
	// Older transcripts have no stop_reason; behavior must not regress to empty.
	jsonl := `{"type":"user","message":{"role":"user","content":"q"}}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"legacy answer"}]}}
`
	turns, _, err := ParseClaudeTurns(write(t, "legacy.jsonl", jsonl))
	if err != nil {
		t.Fatal(err)
	}
	if len(turns) != 1 || turns[0].AssistantText != "legacy answer" {
		t.Fatalf("got %+v", turns)
	}
}

func TestParseClaudeTurns_perTurnAccumulatorIsReset(t *testing.T) {
	// Turn 2 must not inherit turn 1's final text.
	jsonl := `{"type":"user","message":{"role":"user","content":"q1"}}
{"type":"assistant","message":{"role":"assistant","stop_reason":"end_turn","content":[{"type":"text","text":"a1"}]}}
{"type":"user","message":{"role":"user","content":"q2"}}
{"type":"assistant","message":{"role":"assistant","stop_reason":"end_turn","content":[{"type":"text","text":"a2"}]}}
`
	turns, _, err := ParseClaudeTurns(write(t, "reset.jsonl", jsonl))
	if err != nil {
		t.Fatal(err)
	}
	if len(turns) != 2 {
		t.Fatalf("got %d turns, want 2", len(turns))
	}
	if turns[0].AssistantText != "a1" || turns[1].AssistantText != "a2" {
		t.Errorf("turns = %q / %q, want a1 / a2", turns[0].AssistantText, turns[1].AssistantText)
	}
}

func TestParseClaudeTurns_sumsUsageAcrossModelCallsWithoutDuplicateResponses(t *testing.T) {
	jsonl := `{"type":"user","message":{"role":"user","content":"q"}}
{"type":"assistant","uuid":"record-1","message":{"id":"response-1","role":"assistant","stop_reason":"tool_use","usage":{"input_tokens":10,"cache_creation_input_tokens":2,"cache_read_input_tokens":3,"output_tokens":4},"content":[{"type":"text","text":"intermediate"}]}}
{"type":"assistant","uuid":"record-2","message":{"id":"response-1","role":"assistant","stop_reason":"tool_use","usage":{"input_tokens":10,"cache_creation_input_tokens":2,"cache_read_input_tokens":3,"output_tokens":4},"content":[{"type":"tool_use","name":"Bash"}]}}
{"type":"user","message":{"role":"user","content":[{"type":"tool_result","content":"ignored"}]}}
{"type":"assistant","uuid":"record-3","message":{"id":"response-2","role":"assistant","stop_reason":"end_turn","usage":{"input_tokens":20,"cache_creation_input_tokens":0,"cache_read_input_tokens":5,"output_tokens":6},"content":[{"type":"text","text":"final answer"}]}}
`
	turns, _, err := ParseClaudeTurns(write(t, "usage.jsonl", jsonl))
	if err != nil {
		t.Fatal(err)
	}
	if len(turns) != 1 {
		t.Fatalf("got %d turns, want 1", len(turns))
	}
	if turns[0].AssistantText != "final answer" {
		t.Errorf("AssistantText = %q, want final answer", turns[0].AssistantText)
	}
	if turns[0].TokenCount != 50 { // response-1: 19, response-2: 31
		t.Errorf("TokenCount = %d, want 50", turns[0].TokenCount)
	}
}

func TestParseClaudeTurns_skipsCaveatWrapper(t *testing.T) {
	jsonl := `{"type":"user","message":{"role":"user","content":"<local-command-caveat>Caveat: generated</local-command-caveat>"}}
{"type":"user","message":{"role":"user","content":"real prompt"}}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"ok"}]}}
`
	turns, _, err := ParseClaudeTurns(write(t, "c2.jsonl", jsonl))
	if err != nil {
		t.Fatal(err)
	}
	if len(turns) != 1 || turns[0].UserText != "real prompt" {
		t.Fatalf("got %+v, want single 'real prompt'", turns)
	}
}

func TestParseCodexTurns_basic(t *testing.T) {
	// Real prompts arrive as event_msg user_message / agent_message. An injected
	// AGENTS.md user_message and a stray leading agent_message must be skipped.
	jsonl := `{"type":"session_meta","timestamp":"t0","payload":{"cwd":"/ws/cx","session_id":"s1"}}
{"type":"event_msg","timestamp":"t0","payload":{"type":"task_started","turn_id":"x"}}
{"type":"event_msg","timestamp":"t1","payload":{"type":"user_message","message":"# AGENTS.md instructions\n<INSTRUCTIONS>...</INSTRUCTIONS>"}}
{"type":"event_msg","timestamp":"t2","payload":{"type":"user_message","message":"do the thing"}}
{"type":"event_msg","timestamp":"t2","payload":{"type":"agent_message","message":"done","phase":"final"}}
{"type":"event_msg","timestamp":"t3","payload":{"type":"user_message","message":"and again"}}
{"type":"event_msg","timestamp":"t3","payload":{"type":"agent_message","message":"ok","phase":"final"}}
{"type":"response_item","timestamp":"t3","payload":{"type":"message","role":"developer","content":[{"type":"text","text":"noise ignored"}]}}
`
	turns, cwd, err := ParseCodexTurns(write(t, "r.jsonl", jsonl))
	if err != nil {
		t.Fatal(err)
	}
	if cwd != "/ws/cx" {
		t.Errorf("cwd = %q", cwd)
	}
	if len(turns) != 2 {
		t.Fatalf("got %d turns, want 2: %+v", len(turns), turns)
	}
	if turns[0].UserText != "do the thing" || turns[0].AssistantText != "done" {
		t.Errorf("turn0 = %+v", turns[0])
	}
	if turns[1].UserText != "and again" || turns[1].AssistantText != "ok" {
		t.Errorf("turn1 = %+v", turns[1])
	}
}

func TestParseCodexTurns_usesFinalResponseAndSumsTurnTokens(t *testing.T) {
	jsonl := `{"type":"session_meta","timestamp":"t0","payload":{"cwd":"/ws/cx","session_id":"s1"}}
{"type":"event_msg","timestamp":"t1","payload":{"type":"user_message","message":"do the thing"}}
{"type":"event_msg","timestamp":"t1","payload":{"type":"agent_message","message":"checking first","phase":"commentary"}}
{"type":"response_item","timestamp":"t1","payload":{"type":"custom_tool_call","name":"exec"}}
{"type":"event_msg","timestamp":"t1","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":90,"cached_input_tokens":50,"output_tokens":10,"reasoning_output_tokens":4,"total_tokens":100}}}}
{"type":"response_item","timestamp":"t1","payload":{"type":"custom_tool_call_output","output":"ignored"}}
{"type":"event_msg","timestamp":"t1","payload":{"type":"agent_message","message":"event final","phase":"final_answer"}}
{"type":"event_msg","timestamp":"t1","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":20,"cached_input_tokens":10,"output_tokens":5,"reasoning_output_tokens":2,"total_tokens":25}}}}
{"type":"event_msg","timestamp":"t1","payload":{"type":"task_complete","last_agent_message":"authoritative final"}}
`
	turns, _, err := ParseCodexTurns(write(t, "final-tokens.jsonl", jsonl))
	if err != nil {
		t.Fatal(err)
	}
	if len(turns) != 1 {
		t.Fatalf("got %d turns, want 1", len(turns))
	}
	if turns[0].AssistantText != "authoritative final" {
		t.Errorf("AssistantText = %q, want authoritative final", turns[0].AssistantText)
	}
	if turns[0].TokenCount != 125 {
		t.Errorf("TokenCount = %d, want 125", turns[0].TokenCount)
	}
}

func TestParseCodexTurns_tokenCountFallsBackToInputPlusOutput(t *testing.T) {
	jsonl := `{"type":"event_msg","payload":{"type":"user_message","message":"q"}}
{"type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":12,"cached_input_tokens":8,"output_tokens":3,"reasoning_output_tokens":2}}}}
{"type":"event_msg","payload":{"type":"task_complete","last_agent_message":"done"}}
`
	turns, _, err := ParseCodexTurns(write(t, "fallback-tokens.jsonl", jsonl))
	if err != nil {
		t.Fatal(err)
	}
	if len(turns) != 1 || turns[0].TokenCount != 15 {
		t.Fatalf("got %+v, want TokenCount 15", turns)
	}
}

func TestLocateCodexRollout(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "2026", "07", "18")
	os.MkdirAll(sub, 0o755)
	want := filepath.Join(sub, "rollout-2026-07-18T22-35-10-019f75a7-abc.jsonl")
	os.WriteFile(want, []byte("{}"), 0o644)
	os.WriteFile(filepath.Join(sub, "other.jsonl"), []byte("{}"), 0o644)

	got := LocateCodexRollout(dir, "019f75a7-abc")
	if got != want {
		t.Errorf("located %q, want %q", got, want)
	}
	if LocateCodexRollout(dir, "nope") != "" {
		t.Error("expected empty for missing id")
	}
}
