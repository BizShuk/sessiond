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

	_ "github.com/bizshuk/sessiond/config/example"

	"github.com/spf13/viper"
)

// Package defaults are registered automatically when config is imported.
func init() {
	viper.SetDefault("sessiond.hooks.paused", false)
	viper.SetDefault("sessiond.summarizer.provider", "auto") // auto | heuristic | <any registered provider name, e.g. "minimax">
	viper.SetDefault("sessiond.summarizer.model", "MiniMax-M3")
	viper.SetDefault("sessiond.agents.claude.transcripts_dir", "") // empty → default ~/.claude/projects
	viper.SetDefault("sessiond.agents.codex.sessions_dir", "")     // empty → default ~/.codex/sessions
}

// HooksPaused reports whether hook ingestion is disabled.
func HooksPaused() (bool, error) {
	return viper.GetBool("sessiond.hooks.paused"), nil
}

// SummarizerProvider returns the configured provider name. Accepted values
// are "auto" (default — picks LLM when its key env var is set, else
// heuristic), "heuristic" (forced — never calls an LLM), or any provider
// name registered with the agentsdk provider registry (e.g. "minimax",
// "anthropic", "google"). Callers in internal/hook look up the chosen
// provider's API-key env var via summarize.IsProviderConfigured.
func SummarizerProvider() string {
	if v := os.Getenv("SUPERSET_SUMMARIZER"); v != "" {
		return v
	}
	if v := os.Getenv("APP_SESSIOND_SUMMARIZER_PROVIDER"); v != "" {
		return v
	}
	return viper.GetString("sessiond.summarizer.provider")
}

// SummarizerModel is the model id handed to the chosen agentSDK provider.
// The provider's own catalog validates the id; invalid ids fall through to
// the heuristic summarizer on the first error.
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
