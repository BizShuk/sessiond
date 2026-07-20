package stop

import (
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bizshuk/sessiond/pkg/model"
)

// fixture builds a Claude projects layout that mirrors the real install: one
// workspace dir under <ClaudeDir> named with the leading-dash / path encoding,
// a transcript JSONL that records two user turns. Returns the on-disk paths
// the test needs to drive Run.
type fixtureEnv struct {
	ClaudeDir  string
	Transcript string
	DataDir    string
	SessionFile string
}

func newFixture(t *testing.T) fixtureEnv {
	t.Helper()
	root := t.TempDir()
	workspace := "/ws/proj"
	encoded := strings.ReplaceAll(workspace, "/", "-")
	claudeDir := filepath.Join(root, "claude-projects")
	transcriptDir := filepath.Join(claudeDir, encoded)
	if err := os.MkdirAll(transcriptDir, 0o755); err != nil {
		t.Fatal(err)
	}

	transcript := filepath.Join(transcriptDir, "s-1.jsonl")
	writeJSONL(t, transcript, []map[string]any{
		{"type": "user", "cwd": workspace, "timestamp": "2026-07-20T01:00:00Z",
			"message": map[string]any{"role": "user", "content": "first prompt"}},
		{"type": "assistant", "timestamp": "2026-07-20T01:00:01Z",
			"message": map[string]any{"role": "assistant", "content": []any{
				map[string]any{"type": "text", "text": "answer one"}}}},
		{"type": "user", "timestamp": "2026-07-20T01:00:02Z",
			"message": map[string]any{"role": "user", "content": "second prompt"}},
		{"type": "assistant", "timestamp": "2026-07-20T01:00:03Z",
			"message": map[string]any{"role": "assistant", "content": []any{
				map[string]any{"type": "text", "text": "answer two"}}}},
	})

	dataDir := filepath.Join(root, "data")
	sessionDir := filepath.Join(dataDir, "sessions", strings.ReplaceAll(workspace, "/", "%2F"))
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	sessionFile := filepath.Join(sessionDir, "s-1.jsonl")
	meta := model.Meta{
		Type:          model.RECORD_META,
		Agent:         "claude",
		SessionID:     "s-1",
		WorkspacePath: workspace,
		Title:         "first prompt",
		Resume:        model.Resume{Kind: "terminal", Command: "claude --resume s-1", Cwd: workspace},
		CreatedAt:     "2026-07-20T01:00:00Z",
		SchemaVersion: model.SCHEMA_VERSION,
	}
	writeSingle(t, sessionFile, meta)
	return fixtureEnv{
		ClaudeDir:   claudeDir,
		Transcript:  transcript,
		DataDir:     dataDir,
		SessionFile: sessionFile,
	}
}

func writeJSONL(t *testing.T, path string, lines []map[string]any) {
	t.Helper()
	f, err := os.Create(path)
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
}

func writeSingle(t *testing.T, path string, v any) {
	t.Helper()
	body, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(body, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}

func silentLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	out := []string{}
	for l := range strings.SplitSeq(strings.TrimRight(string(b), "\n"), "\n") {
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

func TestRun_AppendsTurnsAcrossScopes(t *testing.T) {
	env := newFixture(t)
	summary, err := Run(Options{
		DataDir:   env.DataDir,
		ClaudeDir: env.ClaudeDir,
		Logger:    silentLogger(),
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if summary.Scanned != 1 {
		t.Errorf("scanned = %d, want 1", summary.Scanned)
	}
	if summary.Advanced != 1 {
		t.Errorf("advanced = %d, want 1", summary.Advanced)
	}
	if summary.Appended != 2 {
		t.Errorf("appended = %d, want 2", summary.Appended)
	}
	lines := readLines(t, env.SessionFile)
	if len(lines) != 3 {
		t.Errorf("session file should have meta + 2 turn lines, got %d", len(lines))
	}
}

func TestRun_IdempotentSecondCallAdvancesNone(t *testing.T) {
	env := newFixture(t)
	opts := Options{DataDir: env.DataDir, ClaudeDir: env.ClaudeDir, Logger: silentLogger()}
	if _, err := Run(opts); err != nil {
		t.Fatal(err)
	}
	second, err := Run(opts)
	if err != nil {
		t.Fatal(err)
	}
	if second.Advanced != 0 || second.Appended != 0 {
		t.Errorf("idempotent: second call advanced=%d appended=%d, want 0/0", second.Advanced, second.Appended)
	}
}

func TestRun_DryRunDoesNotWrite(t *testing.T) {
	env := newFixture(t)
	before := readLines(t, env.SessionFile)
	summary, err := Run(Options{
		DataDir:   env.DataDir,
		ClaudeDir: env.ClaudeDir,
		Logger:    silentLogger(),
		DryRun:    true,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if summary.Scanned != 1 {
		t.Errorf("dry-run scanned = %d, want 1", summary.Scanned)
	}
	after := readLines(t, env.SessionFile)
	if len(after) != len(before) {
		t.Errorf("dry-run mutated store: lines %d → %d", len(before), len(after))
	}
}

func TestRun_EmptyDataDirIsClean(t *testing.T) {
	root := t.TempDir()
	summary, err := Run(Options{
		DataDir:   filepath.Join(root, "data"),
		ClaudeDir: filepath.Join(root, "claude"),
		Logger:    silentLogger(),
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if summary.Scanned != 0 {
		t.Errorf("empty store: scanned=%d, want 0", summary.Scanned)
	}
}

func TestMatches_ScopeFiltersBySessionID(t *testing.T) {
	meta := model.Meta{Agent: "claude", SessionID: "abc", WorkspacePath: "/w"}
	if !matches(Scope{SessionID: "abc"}, meta) {
		t.Error("session id match should pass")
	}
	if matches(Scope{SessionID: "xyz"}, meta) {
		t.Error("session id mismatch should fail")
	}
	if matches(Scope{Agent: "codex"}, meta) {
		t.Error("agent mismatch should fail")
	}
	if !matches(Scope{}, meta) {
		t.Error("empty scope must match everything")
	}
}
