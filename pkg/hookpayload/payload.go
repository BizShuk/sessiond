// Package hookpayload parses the single-line JSON that both Claude Code and
// Codex pipe to a hook command on stdin. The two agents share nearly the same
// shape; this struct is the tolerant union of both.
package hookpayload

import (
	"encoding/json"
	"io"
)

// Payload is the union of Claude and Codex hook stdin fields. Unknown fields are
// ignored; absent fields stay zero. Never fail hard on a missing field — a hook
// must be best-effort so it never blocks the host agent.
type Payload struct {
	SessionID           string `json:"session_id"`
	TranscriptPath      string `json:"transcript_path"`
	Cwd                 string `json:"cwd"`
	HookEventName       string `json:"hook_event_name"`
	TurnID              string `json:"turn_id"`
	Model               string `json:"model"`
	LastAssistantMsg    string `json:"last_assistant_message"`
	AgentTranscriptPath string `json:"agent_transcript_path"` // SubagentStop: sub transcript (ignored for main stream)
}

// Read parses a hook payload from r (stdin). A malformed or empty body yields a
// zero Payload and no error so the caller can degrade gracefully.
func Read(r io.Reader) (Payload, error) {
	var p Payload
	b, err := io.ReadAll(r)
	if err != nil || len(b) == 0 {
		return p, nil
	}
	_ = json.Unmarshal(b, &p) // tolerate garbage; zero-value fields are fine
	return p, nil
}
