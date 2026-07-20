// Package store persists sessions as append-only JSONL under
//
//	<dataDir>/sessions/<encoded-workspace>/<session_id>.jsonl
//
// The first line is a Meta record; every later line is a Turn. Sync is
// idempotent: it appends only turns not already on disk, so repeated hook fires
// (Stop, SubagentStop, retries) never duplicate lines.
package store

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/bizshuk/sessiond/pkg/model"
)

// EncodeWorkspace maps an absolute path to a single reversible directory
// segment (Grok-style %2F encoding), so listing one dir enumerates all sessions
// of one workspace without deep nesting or path collisions.
func EncodeWorkspace(p string) string {
	if p == "" {
		return "_unknown"
	}
	return strings.ReplaceAll(p, "/", "%2F")
}

// DecodeWorkspace reverses EncodeWorkspace (used by tooling / the extension).
func DecodeWorkspace(seg string) string {
	if seg == "_unknown" {
		return ""
	}
	return strings.ReplaceAll(seg, "%2F", "/")
}

// SessionPath is the JSONL path for a session.
func SessionPath(dataDir string, meta model.Meta) string {
	return filepath.Join(dataDir, "sessions", EncodeWorkspace(meta.WorkspacePath), meta.SessionID+".jsonl")
}

// CountTurns returns how many turn lines are already persisted for a session,
// so callers can summarize only the new tail (important once summarization costs
// money). Zero for an absent file.
func CountTurns(dataDir string, meta model.Meta) int {
	n, _ := countTurns(SessionPath(dataDir, meta))
	return n
}

// Sync writes meta (once, on file creation) and appends any turns whose Index is
// beyond what is already on disk. Returns how many turn lines were appended.
func Sync(dataDir string, meta model.Meta, turns []model.Turn) (appended int, err error) {
	fp := SessionPath(dataDir, meta)
	if err = os.MkdirAll(filepath.Dir(fp), 0o755); err != nil {
		return 0, err
	}

	existing, isNew := countTurns(fp)

	f, err := os.OpenFile(fp, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	enc := json.NewEncoder(f)
	if isNew {
		meta.Type = model.RECORD_META
		meta.SchemaVersion = model.SCHEMA_VERSION
		if err = enc.Encode(meta); err != nil {
			return 0, err
		}
	}
	for _, t := range turns {
		if t.Index <= existing {
			continue // already persisted
		}
		t.Type = model.RECORD_TURN
		if err = enc.Encode(t); err != nil {
			return appended, err
		}
		appended++
	}
	return appended, nil
}

// countTurns returns the number of turn lines already in fp and whether the file
// is new (absent). A missing file yields (0, true).
func countTurns(fp string) (n int, isNew bool) {
	f, err := os.Open(fp)
	if err != nil {
		return 0, true
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		if strings.Contains(sc.Text(), `"type":"turn"`) {
			n++
		}
	}
	_ = sc.Err() // best-effort count; a read error just yields a smaller n
	return n, false
}
