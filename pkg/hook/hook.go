// Package hook is the per-agent hook entry point: read JSON from stdin,
// extract turns, summarize, append to the JSONL store.
//
// Hooks are best-effort telemetry. The agent (Claude/Codex) ignores a normal
// exit, but a failed hook MUST NOT block the host agent — exit 2 on a Claude
// Stop would interrupt the user. So this package:
//   - reads stdin best-effort (zero-value on malformed JSON)
//   - tolerates missing transcripts (zero turns)
//   - swallows store/summarize errors with a slog.Warn (visible in stderr)
//   - always returns nil
//
// Callers should invoke Run directly; main wires slog + config into RunOptions.
package hook

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	gosdkcfg "github.com/bizshuk/gosdk/config"

	sessiondcfg "github.com/bizshuk/sessiond/config"
	"github.com/bizshuk/sessiond/pkg/hookpayload"
	"github.com/bizshuk/sessiond/pkg/ingest"
	"github.com/bizshuk/sessiond/pkg/model"
	"github.com/bizshuk/sessiond/pkg/store"
	"github.com/bizshuk/sessiond/pkg/summarize"
)

// RunOptions configures one invocation. Fields are plumbing for the cobra
// glue and tests; defaults are filled by WithDefaults.
type RunOptions struct {
	Agent        string
	Stdin        io.Reader
	Stdout       io.Writer
	DataDir      string // override GetAppDataDir()
	CodexDir     string // override home + .codex/sessions
	Logger       *slog.Logger
	NewSummarize func() summarize.Summarizer
	Now          func() time.Time
}

// Run executes one hook fire. It always returns nil; failures are logged via
// opts.Logger. The agent may be "claude" or "codex"; any other value yields a
// logged error and a clean exit.
func Run(opts RunOptions) error {
	opts = withDefaults(opts)
	log := opts.Logger

	p, _ := hookpayload.Read(opts.Stdin)
	writeResponse(opts.Stdout, opts.Agent) // must precede any "no-op" returns

	raws, cwd, err := extractTurns(opts, p)
	if err != nil {
		log.Error("extract turns failed", "agent", opts.Agent, "session_id", p.SessionID,
			"event", p.HookEventName, "err", err)
		return nil
	}
	if len(raws) == 0 {
		log.Info("no turns extracted", "agent", opts.Agent, "session_id", p.SessionID)
		return nil
	}

	summarizer := opts.NewSummarize()
	workspace := firstNonEmpty(p.Cwd, cwd)
	meta := model.Meta{
		Agent:         opts.Agent,
		SessionID:     p.SessionID,
		WorkspacePath: workspace,
		Resume:        resumeSpec(opts.Agent, p.SessionID, workspace),
		CreatedAt:     opts.Now().UTC().Format(time.RFC3339),
	}

	existing := store.CountTurns(opts.DataDir, meta)
	if existing == 0 {
		// Title uses cheap heuristic so we never spend an LLM call on a label.
		meta.Title = summarize.Heuristic{}.Summarize(raws[0].UserText, "").Summary
	}
	turns := buildTurns(summarizer, raws, p, existing)

	n, err := store.Sync(opts.DataDir, meta, turns)
	if err != nil {
		log.Error("store sync failed", "agent", opts.Agent, "session_id", p.SessionID, "err", err)
		return nil
	}
	log.Info("session synced",
		"agent", opts.Agent,
		"session_id", p.SessionID,
		"workspace", workspace,
		"total_turns", len(raws),
		"appended", n,
		"summarizer", sourceOf(summarizer),
	)
	return nil
}

// withDefaults fills in the runtime defaults for any unset option. Splitting
// it out keeps Run declarative and makes tests trivial (zero value works).
func withDefaults(o RunOptions) RunOptions {
	if o.Stdin == nil {
		o.Stdin = os.Stdin
	}
	if o.Stdout == nil {
		o.Stdout = os.Stdout
	}
	if o.Logger == nil {
		o.Logger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	}
	if o.DataDir == "" {
		gosdkcfg.Default(gosdkcfg.WithAppName("superset"))
		o.DataDir = gosdkcfg.GetAppDataDir()
	}
	if o.CodexDir == "" {
		if v := sessiondcfg.CodexSessionsDir(); v != "" {
			o.CodexDir = v
		} else {
			home, _ := os.UserHomeDir()
			o.CodexDir = filepath.Join(home, ".codex", "sessions")
		}
	}
	if o.NewSummarize == nil {
		o.NewSummarize = func() summarize.Summarizer {
			provider := strings.ToLower(sessiondcfg.SummarizerProvider())
			if provider == "heuristic" || (provider == "auto" && os.Getenv("GOOGLE_API_KEY") == "") {
				return summarize.Heuristic{}
			}
			g, err := summarize.NewGemini(context.Background(), sessiondcfg.SummarizerModel(), summarize.Heuristic{})
			if err != nil {
				o.Logger.Warn("gemma unavailable, using heuristic", "err", err)
				return summarize.Heuristic{}
			}
			return g
		}
	}
	if o.Now == nil {
		o.Now = func() time.Time { return time.Now() }
	}
	return o
}

// extractTurns dispatches to the per-agent parser. For Codex we may need to
// locate the rollout by session id if the hook did not provide a transcript.
func extractTurns(opts RunOptions, p hookpayload.Payload) ([]ingest.RawTurn, string, error) {
	switch opts.Agent {
	case "claude":
		if p.TranscriptPath == "" {
			return nil, "", fmt.Errorf("no transcript_path in payload")
		}
		return ingest.ParseClaudeTurns(p.TranscriptPath)
	case "codex":
		path := p.TranscriptPath
		if !strings.HasSuffix(path, ".jsonl") || !fileExists(path) {
			path = ingest.LocateCodexRollout(opts.CodexDir, p.SessionID)
		}
		if path == "" {
			return nil, "", fmt.Errorf("no codex rollout for session %s", p.SessionID)
		}
		return ingest.ParseCodexTurns(path)
	default:
		return nil, "", fmt.Errorf("unknown agent %q (want claude|codex)", opts.Agent)
	}
}

// buildTurns summarizes each raw turn beyond `existing` into a persistable
// model.Turn. On a StopFailure event the final turn is marked error.
func buildTurns(s summarize.Summarizer, raws []ingest.RawTurn, p hookpayload.Payload, existing int) []model.Turn {
	out := make([]model.Turn, 0)
	for i := existing; i < len(raws); i++ {
		r := raws[i]
		res := s.Summarize(r.UserText, r.AssistantText)
		status := "ok"
		turnID := ""
		if i == len(raws)-1 { // latest turn carries the event's turn id / status
			turnID = p.TurnID
			if p.HookEventName == "StopFailure" {
				status = "error"
			}
		}
		at := r.At
		if at == "" {
			at = time.Now().UTC().Format(time.RFC3339)
		}
		out = append(out, model.Turn{
			Index:   i + 1,
			TurnID:  turnID,
			Event:   p.HookEventName,
			User:    res.User,
			Summary: res.Summary,
			Source:  res.Source,
			Status:  status,
			At:      at,
		})
	}
	return out
}

// resumeSpec tells the extension how to bring the session back in a terminal.
// Codex's resume flag is version-dependent — verify before relying on it.
func resumeSpec(agent, sessionID, cwd string) model.Resume {
	cmd := ""
	switch agent {
	case "claude":
		cmd = "claude --resume " + sessionID
	case "codex":
		cmd = "codex resume " + sessionID
	}
	return model.Resume{Kind: "terminal", Command: cmd, Cwd: cwd}
}

// writeResponse emits the minimal stdout the agent expects. Codex reads a JSON
// response; Claude is happy with empty stdout.
func writeResponse(w io.Writer, agent string) {
	if agent == "codex" {
		fmt.Fprintln(w, `{"continue": true}`)
	}
}

func sourceOf(s summarize.Summarizer) string {
	if _, ok := s.(*summarize.Gemini); ok {
		return "llm"
	}
	return "heuristic"
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func fileExists(p string) bool {
	if p == "" {
		return false
	}
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}