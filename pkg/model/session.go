// Package model is the shared on-disk contract between the Go sessiond ingestor
// and the VSCode superset extension. One session = one JSONL file whose first
// line is a Meta record and whose remaining lines are appended Turn records.
package model

// RecordType discriminates the kind of a JSONL line.
type RecordType string

const (
	RECORD_META RecordType = "meta"
	RECORD_TURN RecordType = "turn"
)

// SCHEMA_VERSION is bumped when the JSONL shape changes so the extension can
// detect drift instead of silently mis-parsing.
const SCHEMA_VERSION = 1

// Meta is the first line of a session file. Written once, never appended twice.
type Meta struct {
	Type          RecordType `json:"type"` // always "meta"
	Agent         string     `json:"agent"` // claude | codex
	SessionID     string     `json:"session_id"`
	WorkspacePath string     `json:"workspace_path"`
	Title         string     `json:"title"`
	Resume        Resume     `json:"resume"`
	CreatedAt     string     `json:"created_at"`
	SchemaVersion int        `json:"schema_version"`
}

// Resume tells the extension how to bring the session back.
type Resume struct {
	Kind    string `json:"kind"` // "terminal"
	Command string `json:"command"`
	Cwd     string `json:"cwd"`
}

// ToolCall is an optional per-turn tool record. Additive to schema v1: it is
// omitted when empty, so files written before tool capture existed still parse.
// The extension renders one H3 section per entry inside the turn's H2 block.
type ToolCall struct {
	Name       string `json:"name"`
	Input      string `json:"input,omitempty"`
	Result     string `json:"result,omitempty"`
	Status     string `json:"status,omitempty"` // ok | error
	DurationMs int    `json:"duration_ms,omitempty"`
}

// Turn is one appended line: a single summarized turn of the session.
type Turn struct {
	Type    RecordType `json:"type"` // always "turn"
	Index   int        `json:"index"` // 1-based, monotonic
	TurnID  string     `json:"turn_id,omitempty"`
	Event   string     `json:"event"` // Stop | StopFailure | SubagentStop | TaskCompleted
	User    string     `json:"user"` // cleaned user prompt (short)
	Summary string     `json:"summary"` // one-line summary
	Source  string     `json:"source"` // heuristic | llm | native
	Status  string     `json:"status"` // ok | error
	At      string     `json:"at"` // ISO-8601 timestamp
	Tools   []ToolCall `json:"tools,omitempty"`
}
