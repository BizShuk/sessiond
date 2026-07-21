// Package config is sessiond's typed wrapper around gosdk viper. It owns all
// configuration keys (defaults + names) so the rest of the codebase never
// touches os.Getenv or viper directly.
//
// Resolution order (gosdk default):
//
//  1. defaults registered via SetDefault below
//  2. YAML/JSON/env files in ./conf and ~/.config/superset/conf/
//  3. explicit APP_* environment variables checked by the getters below
//
// The config file keys are nested (for example,
// sessiond.summarizer.model). gosdk does not install an env-key replacer, so
// APP_SESSIOND_SUMMARIZER_MODEL does not automatically override that dotted
// key; the getters handle the documented APP_* names explicitly.
//
// (e.g. APP_SESSIOND_SUMMARIZER_MODEL → sessiond.summarizer.model)
package config

import (
	"os"

	"github.com/spf13/viper"
)

// Package defaults are registered automatically when config is imported.
func init() {
	viper.SetDefault("sessiond.hooks.paused", false)
	viper.SetDefault("sessiond.summarizer.provider", "auto") // auto | heuristic | google
	viper.SetDefault("sessiond.summarizer.model", "gemma-4-26b-a4b-it")
	viper.SetDefault("sessiond.agents.claude.transcripts_dir", "") // empty → default ~/.claude/projects
	viper.SetDefault("sessiond.agents.codex.sessions_dir", "")     // empty → default ~/.codex/sessions
}

// HooksPaused reports whether hook ingestion is disabled.
func HooksPaused() (bool, error) {
	return viper.GetBool("sessiond.hooks.paused"), nil
}

// SummarizerProvider returns "auto" (default), "heuristic" (forced), or
// "google" (forced). Callers in internal/hook translate "auto" into the
// presence/absence of GOOGLE_API_KEY.
func SummarizerProvider() string {
	if v := os.Getenv("SUPERSET_SUMMARIZER"); v != "" {
		return v
	}
	if v := os.Getenv("APP_SESSIOND_SUMMARIZER_PROVIDER"); v != "" {
		return v
	}
	return viper.GetString("sessiond.summarizer.provider")
}

// SummarizerModel is the gemma model id handed to agentSDK google.WithModel.
func SummarizerModel() string {
	if v := os.Getenv("SUPERSET_SUMMARIZER_MODEL"); v != "" {
		return v
	}
	if v := os.Getenv("APP_SESSIOND_SUMMARIZER_MODEL"); v != "" {
		return v
	}
	return viper.GetString("sessiond.summarizer.model")
}

// CodexSessionsDir returns the configured override or "" when unset (callers
// fall back to ~/.codex/sessions).
func CodexSessionsDir() string {
	if v := os.Getenv("SUPERSET_CODEX_SESSIONS_DIR"); v != "" {
		return v
	}
	if v := os.Getenv("APP_SESSIOND_AGENTS_CODEX_SESSIONS_DIR"); v != "" {
		return v
	}
	return viper.GetString("sessiond.agents.codex.sessions_dir")
}

// ClaudeTranscriptsDir likewise; defaults to ~/.claude/projects when "".
func ClaudeTranscriptsDir() string {
	if v := os.Getenv("APP_SESSIOND_AGENTS_CLAUDE_TRANSCRIPTS_DIR"); v != "" {
		return v
	}
	return viper.GetString("sessiond.agents.claude.transcripts_dir")
}
