package ingest

import (
	"regexp"
	"strings"
)

// RawTurn is one user->assistant exchange extracted from a transcript, before
// summarization. Assistant text is a best-effort concatenation of text blocks.
type RawTurn struct {
	UserText      string
	AssistantText string
	At            string
	TokenCount    int
}

var tagRe = regexp.MustCompile(`<[^>]+>`)

// cleanUserText strips XML-ish tags and collapses whitespace. It returns "" for
// messages that are pure system wrappers (so the caller skips them as turns).
func cleanUserText(raw string) string {
	low := strings.ToLower(raw)
	// Drop obvious system-injected wrappers entirely (but keep slash commands).
	// Claude: command caveats / env context. Codex: AGENTS.md, plugins,
	// permissions preambles that arrive on the user channel.
	if strings.Contains(low, "local-command-caveat") ||
		strings.Contains(low, "<environment_context>") ||
		strings.Contains(low, "<user_instructions>") ||
		strings.Contains(low, "<recommended_plugins>") ||
		strings.Contains(low, "<permissions instructions>") ||
		strings.HasPrefix(strings.TrimSpace(low), "# agents.md") {
		return ""
	}
	s := tagRe.ReplaceAllString(raw, " ")
	s = strings.Join(strings.Fields(s), " ")
	if strings.HasPrefix(strings.ToLower(s), "caveat:") {
		return ""
	}
	return s
}

func intField(m map[string]any, key string) int {
	switch v := m[key].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	default:
		return 0
	}
}
