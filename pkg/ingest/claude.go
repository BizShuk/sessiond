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

	// Only text from a response that ended the turn is retained. Intermediate
	// commentary, tool calls, tool results, and thinking are deliberately omitted
	// from AssistantText so the summarizer receives the smallest useful payload.
	// Usage is different: every model call belongs to the turn's cost, including
	// calls around tools, so their token counts are aggregated separately.
	var cur *RawTurn
	var final strings.Builder
	usageByMessage := make(map[string]int)
	flush := func() {
		if cur != nil {
			cur.AssistantText = strings.TrimSpace(final.String())
			turns = append(turns, *cur)
			cur = nil
		}
		final.Reset()
		clear(usageByMessage)
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
			if cur == nil {
				continue
			}
			addClaudeUsage(cur, usageByMessage, o, msg)
			if text != "" && isTurnFinal(msg) {
				final.WriteString(text)
				final.WriteByte(' ')
			}
		}
	}
	flush()
	return turns, cwd, sc.Err()
}

// addClaudeUsage adds one model response's usage to a turn. Claude can emit
// multiple transcript records for the same response (for example one text
// record and one tool_use record) with the same message.id and repeated usage.
// Keeping the greatest observed total per message avoids double counting while
// still handling a later record whose usage was updated.
func addClaudeUsage(turn *RawTurn, usageByMessage map[string]int, record, msg map[string]any) {
	usage, ok := msg["usage"].(map[string]any)
	if !ok {
		return
	}
	total := intField(usage, "input_tokens") +
		intField(usage, "cache_creation_input_tokens") +
		intField(usage, "cache_read_input_tokens") +
		intField(usage, "output_tokens")

	id, _ := msg["id"].(string)
	if id == "" {
		id, _ = record["uuid"].(string)
	}
	if id == "" {
		turn.TokenCount += total
		return
	}
	previous := usageByMessage[id]
	if total > previous {
		turn.TokenCount += total - previous
		usageByMessage[id] = total
	}
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
