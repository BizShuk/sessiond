package ingest

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// ParseCodexTurns reads a Codex rollout JSONL and returns its turns plus the
// session cwd. Codex records the real human prompt as an event_msg of type
// "user_message" and the model reply as "agent_message" — these are cleaner than
// the response_item stream, which is padded with injected developer/context
// messages (permissions, recommended_plugins, AGENTS.md). A turn = one
// user_message and the agent_message(s) that follow it.
func ParseCodexTurns(path string) (turns []RawTurn, cwd string, err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, "", err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)

	var cur *RawTurn
	flush := func() {
		if cur != nil {
			cur.AssistantText = strings.TrimSpace(cur.AssistantText)
			turns = append(turns, *cur)
			cur = nil
		}
	}

	for sc.Scan() {
		var o map[string]any
		if json.Unmarshal(sc.Bytes(), &o) != nil {
			continue
		}
		typ, _ := o["type"].(string)
		ts, _ := o["timestamp"].(string)
		p, ok := o["payload"].(map[string]any)
		if !ok {
			continue
		}
		if typ == "session_meta" {
			if c, ok := p["cwd"].(string); ok {
				cwd = c
			}
			continue
		}
		if typ != "event_msg" {
			continue
		}
		switch p["type"] {
		case "user_message":
			msg, _ := p["message"].(string)
			clean := cleanUserText(msg)
			if clean == "" {
				continue // injected wrapper (AGENTS.md, plugins, permissions)
			}
			flush()
			cur = &RawTurn{UserText: clean, At: ts}
		case "agent_message":
			if cur != nil {
				if msg, _ := p["message"].(string); msg != "" {
					cur.AssistantText += msg + " "
				}
			}
		}
	}
	flush()
	return turns, cwd, sc.Err()
}

// LocateCodexRollout finds the rollout JSONL for a session id by scanning
// sessionsDir for a file whose name contains the id (Codex names rollouts
// rollout-<ts>-<id>.jsonl). Returns "" when not found.
func LocateCodexRollout(sessionsDir, sessionID string) string {
	if sessionID == "" {
		return ""
	}
	var found string
	_ = filepath.WalkDir(sessionsDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		name := d.Name()
		if strings.HasPrefix(name, "rollout-") && strings.HasSuffix(name, ".jsonl") &&
			strings.Contains(name, sessionID) {
			found = path
		}
		return nil
	})
	return found
}
