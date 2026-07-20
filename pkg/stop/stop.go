// Package stop implements `sessiond stop`: re-fires the Stop hook (and the
// Codex equivalent) for sessions in the JSONL store, on demand and across every
// workspace. Hooks fire automatically when an agent session ends; this command
// covers the cases where they didn't — the host crashed, hooks were uninstalled
// mid-session, the user wants a manual flush, or the user is sitting in a
// different project and wants to catch up without `cd`-ing into each one.
package stop

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	gosdkcfg "github.com/bizshuk/gosdk/config"

	sessiondcfg "github.com/bizshuk/sessiond/config"
	"github.com/bizshuk/sessiond/pkg/hook"
	"github.com/bizshuk/sessiond/pkg/hookpayload"
	"github.com/bizshuk/sessiond/pkg/ingest"
	"github.com/bizshuk/sessiond/pkg/model"
	"github.com/bizshuk/sessiond/pkg/store"
)

// Scope narrows what gets processed. Empty fields mean "all". SessionID takes
// priority over Workspace when both are set.
type Scope struct {
	SessionID string // match exactly when non-empty
	Workspace string // match exactly when non-empty
	Agent     string // "claude" or "codex" when non-empty
}

// Options controls one invocation. All fields are optional.
type Options struct {
	DataDir   string       // default: gosdk GetAppDataDir()
	CodexDir  string       // default: sessiondcfg.CodexSessionsDir() || ~/.codex/sessions
	ClaudeDir string       // default: sessiondcfg.ClaudeTranscriptsDir() || ~/.claude/projects
	Scope     Scope        // filter; empty = process everything
	DryRun    bool         // discover targets and stop
	Logger    *slog.Logger // default: slog text handler on stderr at Info
	Stdout    io.Writer    // default: os.Stdout
}

// Summary captures what happened. Appended is total turns written across all
// sessions; Advanced is how many sessions had new turns appended.
type Summary struct {
	Scanned  int      // session JSONL files inspected
	Advanced int      // sessions that had at least one new turn appended
	Appended int      // total turns appended to the store
	Skipped  []string // human-readable reasons for sessions that could not be processed
}

// Run walks the store, finds sessions whose transcript still holds turns not
// yet persisted, and re-fires the per-agent Stop hook for each. It is safe to
// run repeatedly — store.Sync is idempotent. Errors processing one session do
// not abort the rest.
func Run(opts Options) (Summary, error) {
	opts = withDefaults(opts)
	log := opts.Logger
	var summary Summary

	targets, err := discover(opts)
	if err != nil {
		return summary, fmt.Errorf("discover sessions: %w", err)
	}
	if opts.DryRun {
		summary.Scanned = len(targets)
		return summary, nil
	}
	if len(targets) == 0 {
		return summary, nil
	}
	log.Info("stop: processing sessions", "count", len(targets))

	for _, target := range targets {
		summary.Scanned++
		transcript, err := locateTranscript(opts, target)
		if err != nil {
			summary.Skipped = append(summary.Skipped,
				fmt.Sprintf("%s/%s: %v", target.Agent(), target.SessionID(), err))
			log.Warn("skip session", "agent", target.Agent(), "session_id", target.SessionID(), "err", err)
			continue
		}
		before := store.CountTurns(opts.DataDir, target.Meta())
		hook.Run(hook.RunOptions{
			Agent:   target.Agent(),
			Stdin:   payloadFor(target, transcript),
			DataDir: opts.DataDir,
			CodexDir: opts.CodexDir,
			Logger:  opts.Logger,
		})
		after := store.CountTurns(opts.DataDir, target.Meta())
		appended := after - before
		if appended > 0 {
			summary.Advanced++
			summary.Appended += appended
			log.Info("session advanced",
				"agent", target.Agent,
				"session_id", target.SessionID,
				"workspace", target.Workspace,
				"appended", appended)
			continue
		}
		log.Info("session already in sync",
			"agent", target.Agent,
			"session_id", target.SessionID,
			"workspace", target.Workspace)
	}

	return summary, nil
}

// target is one session the scope matched. We carry meta so the hook pipeline
// writes a consistent record even if no full transcript exists.
type target struct {
	MetaLine model.Meta
}

func (t target) Meta() model.Meta { return t.MetaLine }
func (t target) Agent() string    { return t.MetaLine.Agent }
func (t target) SessionID() string { return t.MetaLine.SessionID }
func (t target) Workspace() string { return t.MetaLine.WorkspacePath }

// discover walks the store, parses each session's meta line, and keeps the ones
// that match opts.Scope. A malformed session file is logged and skipped — one
// bad record never blocks the rest.
func discover(opts Options) ([]target, error) {
	sessionsDir := filepath.Join(opts.DataDir, "sessions")
	var out []target
	err := filepath.WalkDir(sessionsDir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if errors.Is(walkErr, fs.ErrNotExist) {
				return nil // empty store is fine
			}
			return walkErr
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".jsonl") {
			return nil
		}
		meta, err := readFirstMeta(path)
		if err != nil {
			opts.Logger.Warn("skip unparseable session file", "path", path, "err", err)
			return nil
		}
		if !matches(opts.Scope, meta) {
			return nil
		}
		out = append(out, target{MetaLine: meta})
		return nil
	})
	return out, err
}

// matches reports whether a meta record sits inside opts.Scope. Empty scope
// fields are wildcards; a non-empty SessionID wins over Workspace when both are
// set (so callers can target a session id that's also pinned to a workspace).
func matches(scope Scope, meta model.Meta) bool {
	if scope.Agent != "" && meta.Agent != scope.Agent {
		return false
	}
	if scope.SessionID != "" && meta.SessionID != scope.SessionID {
		return false
	}
	if scope.Workspace != "" && meta.WorkspacePath != scope.Workspace {
		return false
	}
	return true
}

// locateTranscript finds the on-disk transcript for a target. Both layouts use a
// predictable file name (`<session-id>.jsonl` for Claude, `rollout-*-<id>.jsonl`
// for Codex) — we resolve Claude via its fixed projects dir and Codex via the
// existing LocateCodexRollout helper.
func locateTranscript(opts Options, t target) (string, error) {
	switch t.Agent() {
	case "claude":
		dir := claudeProjectsDir(opts.ClaudeDir)
		candidate := filepath.Join(dir, encodedClaudeWorkspace(t.Workspace()), t.SessionID()+".jsonl")
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("stat claude transcript: %w", err)
		}
		// Fallback: scan the whole projects tree — Claude's directory naming has
		// shifted over versions and the simple encoded form isn't always right.
		var found string
		_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil || d.IsDir() {
				return nil
			}
			if d.Name() == t.SessionID()+".jsonl" {
				found = path
			}
			return nil
		})
		if found != "" {
			return found, nil
		}
		return "", fmt.Errorf("claude transcript not found under %s", dir)
	case "codex":
		path := ingest.LocateCodexRollout(opts.CodexDir, t.SessionID())
		if path == "" {
			return "", fmt.Errorf("codex rollout not found under %s", opts.CodexDir)
		}
		return path, nil
	default:
		return "", fmt.Errorf("unsupported agent %q in meta", t.Agent())
	}
}

// payloadFor renders a JSON line on io.Reader the way an agent would. The hook
// pipeline reads this exactly as if Claude/Codex had fired Stop natively.
func payloadFor(t target, transcript string) io.Reader {
	payload := hookpayload.Payload{
		SessionID:      t.SessionID(),
		TranscriptPath: transcript,
		Cwd:            t.Workspace(),
		HookEventName:  "Stop",
	}
	body, err := json.Marshal(payload)
	if err != nil {
		// Marshal of a fixed-shape struct cannot fail in practice; treat as
		// empty stdin so the hook degrades to "no payload, no-op".
		body = nil
	}
	return strings.NewReader(string(body))
}

// readFirstMeta parses just the meta record from the front of a session JSONL.
// A trailing newline is required; missing records are an error.
func readFirstMeta(path string) (model.Meta, error) {
	f, err := os.Open(path)
	if err != nil {
		return model.Meta{}, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	if !sc.Scan() {
		if err := sc.Err(); err != nil {
			return model.Meta{}, fmt.Errorf("scan: %w", err)
		}
		return model.Meta{}, errors.New("empty session file")
	}
	var meta model.Meta
	if err := json.Unmarshal(sc.Bytes(), &meta); err != nil {
		return model.Meta{}, fmt.Errorf("unmarshal meta: %w", err)
	}
	if meta.Type != model.RECORD_META {
		return model.Meta{}, fmt.Errorf("first record type=%q, want meta", meta.Type)
	}
	if meta.SessionID == "" {
		return model.Meta{}, errors.New("meta missing session_id")
	}
	return meta, nil
}

// encodedClaudeWorkspace mirrors the convention in docs/session/claude-transcript.md
// (this repository's docs/ subtree).
// "/" → "-", preserving the leading "-".
func encodedClaudeWorkspace(workspace string) string {
	if workspace == "" {
		return ""
	}
	return strings.ReplaceAll(workspace, "/", "-")
}

func claudeProjectsDir(override string) string {
	if override != "" {
		return override
	}
	if dir := sessiondcfg.ClaudeTranscriptsDir(); dir != "" {
		return dir
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude", "projects")
}

func withDefaults(o Options) Options {
	if o.DataDir == "" {
		gosdkcfg.Default(gosdkcfg.WithAppName("superset"))
		o.DataDir = gosdkcfg.GetAppDataDir()
	}
	if o.CodexDir == "" {
		if dir := sessiondcfg.CodexSessionsDir(); dir != "" {
			o.CodexDir = dir
		} else {
			home, _ := os.UserHomeDir()
			o.CodexDir = filepath.Join(home, ".codex", "sessions")
		}
	}
	if o.ClaudeDir == "" {
		o.ClaudeDir = claudeProjectsDir("")
	}
	if o.Logger == nil {
		o.Logger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	}
	if o.Stdout == nil {
		o.Stdout = os.Stdout
	}
	return o
}
