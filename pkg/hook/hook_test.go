package hook

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bizshuk/sessiond/pkg/model"
	"github.com/bizshuk/sessiond/pkg/summarize"
)

// fixtureClaudeTranscript writes a minimal Claude JSONL transcript that has
// two real user turns and one sidechain (skipped) line. Returns the path.
func fixtureClaudeTranscript(t *testing.T) string {
	t.Helper()
	lines := []map[string]any{
		{"type": "user", "cwd": "/ws/proj", "timestamp": "2026-07-20T01:00:00Z",
			"message": map[string]any{"role": "user", "content": "first prompt"}},
		{"type": "assistant", "timestamp": "2026-07-20T01:00:01Z",
			"message": map[string]any{"role": "assistant", "content": []any{
				map[string]any{"type": "text", "text": "answer one"}}}},
		{"type": "user", "isSidechain": true, "timestamp": "2026-07-20T01:00:02Z",
			"message": map[string]any{"role": "user", "content": "ignored sidechain"}},
		{"type": "user", "timestamp": "2026-07-20T01:00:03Z",
			"message": map[string]any{"role": "user", "content": "second prompt"}},
		{"type": "assistant", "timestamp": "2026-07-20T01:00:04Z",
			"message": map[string]any{"role": "assistant", "content": []any{
				map[string]any{"type": "text", "text": "answer two"}}}},
	}
	return writeJSONL(t, "c.jsonl", lines)
}

func writeJSONL(t *testing.T, name string, lines []map[string]any) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for _, l := range lines {
		if err := enc.Encode(l); err != nil {
			t.Fatal(err)
		}
	}
	return p
}

// stubSummarizer records what it was asked and returns a fixed result.
type stubSummarizer struct {
	calls int
	tag   string // value put in Summary, useful to prove which backend ran
}

func (s *stubSummarizer) Summarize(userText, assistantText string) summarize.Result {
	s.calls++
	return summarize.Result{
		User:    userText,
		Summary: s.tag + ":" + userText,
		Source:  "stub",
	}
}

func newTestLogger() (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	return slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})), &buf
}

func newStdin(s string) io.Reader { return strings.NewReader(s) }

func TestRun_Claude_HappyPath_AppendsTurnsAndExitsClean(t *testing.T) {
	transcript := fixtureClaudeTranscript(t)
	dataDir := t.TempDir()
	sum := &stubSummarizer{tag: "llm"}
	log, logBuf := newTestLogger()
	var stdout bytes.Buffer
	fixed := time.Date(2026, 7, 20, 2, 0, 0, 0, time.UTC)

	err := Run(RunOptions{
		Agent:   "claude",
		Stdin:   newStdin(`{"session_id":"s-1","transcript_path":"` + transcript + `","cwd":"/ws/proj","hook_event_name":"Stop"}`),
		Stdout:  &stdout,
		DataDir: dataDir,
		Logger:  log,
		NewSummarize: func() summarize.Summarizer { return sum },
		Now:     func() time.Time { return fixed },
	})
	if err != nil {
		t.Fatalf("Run returned err=%v, expected nil (exit 0 contract)", err)
	}

	// Codex response is only emitted for codex; for claude stdout is empty.
	if stdout.Len() != 0 {
		t.Errorf("expected no stdout for claude hook, got %q", stdout.String())
	}

	// JSONL should have one meta line + two turn lines.
	files, _ := filepath.Glob(filepath.Join(dataDir, "sessions", "*", "s-1.jsonl"))
	if len(files) != 1 {
		t.Fatalf("expected 1 session file, got %d (dataDir=%s)", len(files), dataDir)
	}
	lines := readLines(t, files[0])
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines (meta+2 turns), got %d: %v", len(lines), lines)
	}

	var meta model.Meta
	if err := json.Unmarshal([]byte(lines[0]), &meta); err != nil {
		t.Fatalf("meta parse: %v", err)
	}
	if meta.Agent != "claude" || meta.SessionID != "s-1" || meta.WorkspacePath != "/ws/proj" {
		t.Errorf("meta wrong: %+v", meta)
	}
	if meta.Resume.Kind != "terminal" || !strings.HasPrefix(meta.Resume.Command, "claude --resume s-1") {
		t.Errorf("resume spec wrong: %+v", meta.Resume)
	}

	var turn1, turn2 model.Turn
	_ = json.Unmarshal([]byte(lines[1]), &turn1)
	_ = json.Unmarshal([]byte(lines[2]), &turn2)
	if turn1.Index != 1 || turn1.Summary != "llm:first prompt" || turn1.Event != "Stop" {
		t.Errorf("turn1 = %+v", turn1)
	}
	if turn2.Index != 2 || turn2.Summary != "llm:second prompt" {
		t.Errorf("turn2 = %+v", turn2)
	}
	if sum.calls != 2 {
		t.Errorf("summarizer called %d times, want 2", sum.calls)
	}
	if !strings.Contains(logBuf.String(), "session synced") {
		t.Errorf("log missing success line, got: %s", logBuf.String())
	}
}

func TestRun_Idempotent_RunsAreReSyncs(t *testing.T) {
	transcript := fixtureClaudeTranscript(t)
	dataDir := t.TempDir()
	sum := &stubSummarizer{tag: "llm"}
	opts := func() RunOptions {
		return RunOptions{
			Agent:   "claude",
			Stdin:   newStdin(`{"session_id":"s-2","transcript_path":"` + transcript + `","cwd":"/ws/proj","hook_event_name":"Stop"}`),
			DataDir: dataDir,
			Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
			NewSummarize: func() summarize.Summarizer { return sum },
			Now:     func() time.Time { return time.Unix(0, 0) },
		}
	}

	if err := Run(opts()); err != nil {
		t.Fatalf("first Run: %v", err)
	}
	firstCalls := sum.calls

	if err := Run(opts()); err != nil {
		t.Fatalf("second Run: %v", err)
	}
	secondCalls := sum.calls - firstCalls
	if secondCalls != 0 {
		t.Errorf("second fire re-summarized %d turns, want 0 (idempotent)", secondCalls)
	}

	files, _ := filepath.Glob(filepath.Join(dataDir, "sessions", "*", "s-2.jsonl"))
	if got := len(readLines(t, files[0])); got != 3 {
		t.Errorf("expected 3 lines after two fires (meta+2), got %d", got)
	}
}

func TestRun_StopFailure_MarksLastTurnError(t *testing.T) {
	transcript := fixtureClaudeTranscript(t)
	dataDir := t.TempDir()
	var stdout bytes.Buffer
	opts := RunOptions{
		Agent:   "claude",
		Stdin:   newStdin(`{"session_id":"s-3","transcript_path":"` + transcript + `","hook_event_name":"StopFailure"}`),
		Stdout:  &stdout,
		DataDir: dataDir,
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		NewSummarize: func() summarize.Summarizer { return &stubSummarizer{tag: "h"} },
		Now:     func() time.Time { return time.Unix(0, 0) },
	}
	if err := Run(opts); err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob(filepath.Join(dataDir, "sessions", "*", "s-3.jsonl"))
	lines := readLines(t, files[0])
	var last model.Turn
	_ = json.Unmarshal([]byte(lines[len(lines)-1]), &last)
	if last.Status != "error" {
		t.Errorf("StopFailure: last turn status = %q, want error", last.Status)
	}
	if last.Event != "StopFailure" {
		t.Errorf("last turn event = %q, want StopFailure", last.Event)
	}
}

func TestRun_Codex_EmitsContinueResponse(t *testing.T) {
	transcript := writeJSONL(t, "r.jsonl", []map[string]any{
		{"type": "session_meta", "timestamp": "t0", "payload": map[string]any{"cwd": "/ws/cx"}},
		{"type": "event_msg", "timestamp": "t1", "payload": map[string]any{"type": "user_message", "message": "do x"}},
		{"type": "event_msg", "timestamp": "t1", "payload": map[string]any{"type": "agent_message", "message": "ok"}},
	})
	dataDir := t.TempDir()
	var stdout bytes.Buffer
	opts := RunOptions{
		Agent:   "codex",
		Stdin:   newStdin(`{"session_id":"s-codex","transcript_path":"` + transcript + `","hook_event_name":"Stop"}`),
		Stdout:  &stdout,
		DataDir: dataDir,
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		NewSummarize: func() summarize.Summarizer { return &stubSummarizer{tag: "h"} },
		Now:     func() time.Time { return time.Unix(0, 0) },
	}
	if err := Run(opts); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), `"continue": true`) {
		t.Errorf("codex stdout missing continue:true, got %q", stdout.String())
	}
}

func TestRun_UnknownAgent_LogsAndExitsNil(t *testing.T) {
	log, buf := newTestLogger()
	err := Run(RunOptions{
		Agent:   "wat",
		Stdin:   newStdin(`{}`),
		DataDir: t.TempDir(),
		Logger:  log,
	})
	if err != nil {
		t.Errorf("unknown agent must not error, got %v", err)
	}
	if !strings.Contains(buf.String(), "unknown agent") {
		t.Errorf("log missing unknown-agent message, got: %s", buf.String())
	}
}

func TestRun_MalformedStdin_DoesNotPanic(t *testing.T) {
	log, _ := newTestLogger()
	// Garbage on stdin must not panic or return error.
	err := Run(RunOptions{
		Agent:   "claude",
		Stdin:   newStdin("not-json"),
		DataDir: t.TempDir(),
		Logger:  log,
		NewSummarize: func() summarize.Summarizer { return &stubSummarizer{tag: "h"} },
	})
	if err != nil {
		t.Errorf("malformed stdin must not error, got %v", err)
	}
}

// readLines reads a file into string slices (one per line). Tests use it as a
// tiny inline helper rather than dragging in bufio setup.
func readLines(t *testing.T, path string) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, l := range bytes.Split(bytes.TrimRight(b, "\n"), []byte("\n")) {
		if len(l) > 0 {
			out = append(out, string(l))
		}
	}
	return out
}
