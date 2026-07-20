package ingest

import (
	"bufio"
	"encoding/json"
	"os"
	"strings"
)

// ParseClaudeTurns reads a Claude Code transcript JSONL and returns its turns in
// order plus the session cwd (if the transcript records one). A turn starts at
// each real user prompt and absorbs the assistant text that follows until the
// next real user prompt. Tool-result user lines and sidechain lines are skipped.
func ParseClaudeTurns(path string) (turns []RawTurn, cwd string, err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, "", err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024) // tolerate long lines

	// A turn's assistant text is collected twice: `final` holds only the text of
	// records that ended the turn (stop_reason end_turn/stop_sequence), `any`
	// holds every text block. The final text is the actual answer; intermediate
	// text is narration emitted before a tool call ("let me check X first"), and
	// across transcripts it outnumbers final text ~7:1. We prefer final, but fall
	// back to any so an interrupted turn (no end_turn record) still yields text.
	var cur *RawTurn
	var final, anyText strings.Builder
	flush := func() {
		if cur != nil {
			picked := final.String()
			if strings.TrimSpace(picked) == "" {
				picked = anyText.String()
			}
			cur.AssistantText = strings.TrimSpace(picked)
			turns = append(turns, *cur)
			cur = nil
		}
		final.Reset()
		anyText.Reset()
	}

	for sc.Scan() {
		var o map[string]any
		if json.Unmarshal(sc.Bytes(), &o) != nil {
			continue
		}
		if b, _ := o["isSidechain"].(bool); b {
			continue
		}
		if cwd == "" {
			if c, ok := o["cwd"].(string); ok {
				cwd = c
			}
		}
		typ, _ := o["type"].(string)
		msg, ok := o["message"].(map[string]any)
		if !ok {
			continue
		}
		role, _ := msg["role"].(string)
		ts, _ := o["timestamp"].(string)
		text, onlyToolResult := extractClaudeContent(msg["content"])

		switch {
		case typ == "user" && role == "user":
			if onlyToolResult {
				continue // tool_result echo, not a new prompt
			}
			clean := cleanUserText(text)
			if clean == "" {
				continue // system wrapper / caveat
			}
			flush()
			cur = &RawTurn{UserText: clean, At: ts}
		case typ == "assistant" && role == "assistant":
			if cur == nil || text == "" {
				continue
			}
			anyText.WriteString(text)
			anyText.WriteByte(' ')
			if isTurnFinal(msg) {
				final.WriteString(text)
				final.WriteByte(' ')
			}
		}
	}
	flush()
	return turns, cwd, sc.Err()
}

// isTurnFinal reports whether an assistant message ended the turn rather than
// handing off to a tool. `end_turn` is the normal finish; `stop_sequence` is a
// finish triggered by a stop sequence. Anything else — notably `tool_use` — means
// more of the turn follows. A missing stop_reason is treated as final so older
// or truncated transcripts degrade to the previous "collect everything" behavior.
func isTurnFinal(msg map[string]any) bool {
	sr, ok := msg["stop_reason"].(string)
	if !ok || sr == "" {
		return true
	}
	return sr == "end_turn" || sr == "stop_sequence"
}

// extractClaudeContent flattens message.content (string or block array) into
// text. onlyToolResult is true when the array holds tool_result blocks and no
// text — i.e. an assistant tool call being answered, not a human prompt.
func extractClaudeContent(content any) (text string, onlyToolResult bool) {
	switch c := content.(type) {
	case string:
		return c, false
	case []any:
		var b strings.Builder
		sawText, sawToolResult := false, false
		for _, it := range c {
			m, ok := it.(map[string]any)
			if !ok {
				continue
			}
			switch m["type"] {
			case "text":
				if t, ok := m["text"].(string); ok {
					b.WriteString(t)
					b.WriteString(" ")
					sawText = true
				}
			case "tool_result":
				sawToolResult = true
			}
		}
		return strings.TrimSpace(b.String()), sawToolResult && !sawText
	}
	return "", false
}
