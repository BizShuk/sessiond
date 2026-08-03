package summarize

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/bizshuk/agentsdk/core"
	"github.com/bizshuk/agentsdk/provider"

	// Register the bundled providers at process start. Adding a new
	// provider means a new blank import here; switch by changing the
	// LLMOptions.Provider string, not by editing this file.
	_ "github.com/bizshuk/agentsdk/provider/minimax"
)

// LLM is a provider-agnostic LLM-backed summarizer. The actual model
// endpoint is resolved through the agentsdk provider registry from
// LLMOptions.Provider; switching providers means changing the Provider
// string passed at construction, not editing this file.
type LLM struct {
	p        generator
	fallback Summarizer
	timeout  time.Duration
}

// LLMOptions is the full set of knobs callers can pass to NewLLM. Provider
// is the agentsdk registry name (e.g. "minimax", "anthropic", "google");
// Model is forwarded as provider.Options.Model. Timeout defaults to 20s
// when zero, matching the previous hardcoded value.
type LLMOptions struct {
	Provider string
	Model    string
	Timeout  time.Duration
}

type generator interface {
	Generate(context.Context, core.ModelRequest) (core.ModelResult, error)
}

// NewLLM builds an LLM summarizer backed by the named agentsdk provider.
// Returns an error when the provider can not be constructed (e.g. unknown
// name, missing credential); callers typically fall back to Heuristic on
// error.
//
// The provider registry resolves the provider's canonical API-key env var
// and constructs the adapter from its canonical provider.Options pipeline.
func NewLLM(opts LLMOptions, fallback Summarizer) (*LLM, error) {
	timeout := opts.Timeout
	if timeout == 0 {
		timeout = 20 * time.Second
	}
	p, err := provider.New(opts.Provider, provider.Options{Model: opts.Model})
	if err != nil {
		return nil, err
	}
	return &LLM{p: p, fallback: fallback, timeout: timeout}, nil
}

// Summarize attempts one provider call. Rate limits are returned as
// ErrRateLimited so ProcessQueue can pause without discarding the event; other
// provider failures retain the existing heuristic fallback behavior.
func (l *LLM) Summarize(
	ctx context.Context,
	userText string,
	assistantText string,
) (Result, error) {
	requestCtx, cancel := context.WithTimeout(ctx, l.timeout)
	defer cancel()

	prompt := llmSystemPrompt +
		"\n\n[使用者]\n" + truncate(userText, 3000) +
		"\n\n[助手]\n" + truncate(assistantText, 3000)

	res, err := l.p.Generate(requestCtx, core.ModelRequest{
		Messages: []core.Message{{
			Role:  core.ROLE_USER,
			Parts: []core.Part{{Kind: core.PART_KIND_PLAIN_TEXT, Text: prompt}},
		}},
		// 800 leaves room for a "<thought>...</thought>" scratchpad that
		// some models emit before the real summary, plus the actual
		// sentence. 96 was too tight and got cut off mid-thought.
		MaxTokens: 800,
	})
	if err != nil {
		if ctx.Err() != nil {
			return Result{}, ctx.Err()
		}
		if strings.Contains(strings.ToLower(err.Error()), "status 429") {
			return Result{}, fmt.Errorf("%w: %v", ErrRateLimited, err)
		}
		return l.fallback.Summarize(ctx, userText, assistantText)
	}
	summary := stripThought(strings.TrimSpace(res.Text))
	if summary == "" {
		return l.fallback.Summarize(ctx, userText, assistantText)
	}
	return Result{
		User:    truncate(userText, 120),
		Summary: truncate(firstLine(summary), 80),
		Source:  "llm",
	}, nil
}

const llmSystemPrompt = `你是 AI coding session 的摘要器。把以下一個 turn(使用者指令 + 助手回應)濃縮成一句繁體中文摘要,聚焦「使用者想做什麼、結果如何」,不超過 200 字,不要引號、不要條列、不要前綴、不要輸出思考過程或 <thought> 標籤,直接給出最終的一句摘要。`

// IsProviderConfigured reports whether the named provider has a usable
// credential in the current environment. Lets callers decide whether to
// construct an LLM summarizer without hardcoding any specific env var name
// (e.g. "MINIMAX_API_KEY"). Returns false for unknown providers.
//
// Credential resolution is delegated to provider.Options.Resolve — the
// agentsdk primitive exported for exactly this preflight question — so
// OAuth-vs-API-key precedence lives in one place rather than being
// reimplemented here. Providers that need no credential are always ready.
func IsProviderConfigured(name string) bool {
	entry, ok := provider.Lookup(name)
	if !ok {
		return false
	}
	if !entry.Metadata.CredentialRequired {
		return true
	}
	resolved, err := provider.Options{}.Resolve(entry.Metadata)
	if err != nil {
		return false
	}
	return resolved.Auth.Token() != ""
}

// stripThought removes a leading "<thought>...</thought>" scratchpad block.
// Despite llmSystemPrompt asking for a bare final sentence, models
// occasionally still emit a reasoning preamble in that literal tag before
// (or instead of) the actual summary. Two cases:
//   - closed block ("<thought>...</thought>rest") — drop the block, keep
//     whatever text surrounds it (usually the real summary follows).
//   - unclosed block (MaxTokens ran out before "</thought>") — the model
//     never produced an answer at all, so the whole string is discarded and
//     Summarize falls back to Heuristic.
func stripThought(s string) string {
	const openTag = "<thought>"
	i := strings.Index(strings.ToLower(s), openTag)
	if i < 0 {
		return s
	}
	rest := s[i+len(openTag):]
	const closeTag = "</thought>"
	j := strings.Index(strings.ToLower(rest), closeTag)
	if j < 0 {
		return ""
	}
	return strings.TrimSpace(s[:i] + rest[j+len(closeTag):])
}
