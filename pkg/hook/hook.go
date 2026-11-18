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
	HooksPaused  func() (bool, error)
}

// Run executes one hook fire. It always returns nil; failures are logged via
// opts.Logger. The agent may be "claude" or "codex"; any other value yields a
// logged error and a clean exit.
func Run(opts RunOptions) error {
	return RunContext(context.Background(), opts)
}

// RunContext executes one hook fire and allows a rate-limit pause to be
// cancelled when the host process is shutting down.
func RunContext(ctx context.Context, opts RunOptions) error {
	opts = withDefaults(opts)
	log := opts.Logger

	writeResponse(opts.Stdout, opts.Agent) // must precede any "no-op" returns
	paused, err := opts.HooksPaused()
	if err != nil {
		log.Warn("read hook pause state failed; continuing", "agent", opts.Agent, "err", err)
	} else if paused {
		log.Info("hook ingestion paused", "agent", opts.Agent)
		return nil
	}

	p, _ := hookpayload.Read(opts.Stdin)

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
		res, _ := summarize.Heuristic{}.Summarize(ctx, raws[0].UserText, "")
		meta.Title = res.Summary
	}
	turns, err := buildTurns(ctx, summarizer, raws, p, existing)
	if err != nil {
		log.Warn(
			"summarize queued turns failed",
			"agent", opts.Agent,
			"session_id", p.SessionID,
			"err", err,
		)
		return nil
	}

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
		sessiondcfg.Load()
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
			providerName := strings.ToLower(sessiondcfg.SummarizerProvider())
			if providerName == "heuristic" {
				return summarize.Heuristic{}
			}
			if !summarize.IsProviderConfigured(providerName) {
				if providerName != "auto" {
					o.Logger.Warn("summarizer provider not configured, using heuristic", "provider", providerName)
				}
				return summarize.Heuristic{}
			}
			g, err := summarize.NewLLM(summarize.LLMOptions{
				Provider: providerName,
				Model:    sessiondcfg.SummarizerModel(),
			}, summarize.Heuristic{})
			if err != nil {
				o.Logger.Warn("LLM provider unavailable, using heuristic", "provider", providerName, "err", err)
				return summarize.Heuristic{}
			}
			return g
		}
	}
	if o.HooksPaused == nil {
		o.HooksPaused = sessiondcfg.HooksPaused
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

// buildTurns queues each raw turn beyond existing for FIFO summarization.
func buildTurns(
	ctx context.Context,
	s summarize.Summarizer,
	raws []ingest.RawTurn,
	p hookpayload.Payload,
	existing int,
) ([]model.Turn, error) {
	if existing >= len(raws) {
		return nil, nil
	}
	events := make([]summarize.Event, 0, len(raws)-existing)
	for i := existing; i < len(raws); i++ {
		r := raws[i]
		events = append(events, summarize.Event{
			UserText:      r.UserText,
			AssistantText: r.AssistantText,
		})
	}
	results, err := summarize.ProcessQueue(ctx, s, events)
	if err != nil {
		return nil, fmt.Errorf("process summary queue: %w", err)
	}
	return turnsFromResults(raws, results, p, existing), nil
}

// turnsFromResults maps queue results back to the append-only store contract.
// On a StopFailure event the final turn is marked error.
func turnsFromResults(
	raws []ingest.RawTurn,
	results []summarize.Result,
	p hookpayload.Payload,
	existing int,
) []model.Turn {
	out := make([]model.Turn, 0, len(results))
	for offset, res := range results {
		i := existing + offset
		r := raws[i]
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
			Index:      i + 1,
			TurnID:     turnID,
			Event:      p.HookEventName,
			User:       res.User,
			Summary:    res.Summary,
			Source:     res.Source,
			Status:     status,
			At:         at,
			TokenCount: r.TokenCount,
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
	if _, ok := s.(*summarize.LLM); ok {
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
