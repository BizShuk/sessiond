package summarize

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/bizshuk/agentsdk/core"
	"github.com/bizshuk/agentsdk/provider"
	_ "github.com/bizshuk/agentsdk/provider/google"
)

// DEFAULT_GEMMA_MODEL is the gemma id passed to the Google Generative Language
// API. Tentative — verify a currently-served id. Any invalid id makes Generate
// error, which degrades to the fallback summarizer, so a wrong default is safe.
const DEFAULT_GEMMA_MODEL = "gemma-4-26b-a4b-it"

const geminiSystemPrompt = `你是 AI coding session 的摘要器。把以下一個 turn(使用者指令 + 助手回應)濃縮成一句繁體中文摘要,聚焦「使用者想做什麼、結果如何」,不超過 200 字,不要引號、不要條列、不要前綴、不要輸出思考過程或 <thought> 標籤,直接給出最終的一句摘要。`

// Gemini summarizes via the agentSDK google provider (gemma model). Direct
// calls fall back on any provider error; ProcessQueue uses Summarize to
// distinguish rate limits from errors that should fall back immediately.
type Gemini struct {
	p        generator
	fallback Summarizer
	timeout  time.Duration
}

type generator interface {
	Generate(context.Context, core.ModelRequest) (core.ModelResult, error)
}

// NewGemini builds a Gemini backend. It returns an error when the provider can
// not be constructed (e.g. GOOGLE_API_KEY unset); callers then keep Heuristic.
//
// The provider registry resolves GOOGLE_API_KEY and constructs the Google
// adapter from its canonical provider.Options pipeline. The context parameter
// remains for compatibility with current callers and is intentionally unused.
func NewGemini(_ context.Context, model string, fallback Summarizer) (*Gemini, error) {
	if model == "" {
		model = DEFAULT_GEMMA_MODEL
	}
	p, err := provider.New("google", provider.Options{Model: model})
	if err != nil {
		return nil, err
	}
	return &Gemini{p: p, fallback: fallback, timeout: 20 * time.Second}, nil
}

// Summarize attempts one provider call. Rate limits are returned as
// ErrRateLimited so ProcessQueue can pause without discarding the event; other
// provider failures retain the existing heuristic fallback behavior.
func (g *Gemini) Summarize(
	ctx context.Context,
	userText string,
	assistantText string,
) (Result, error) {
	requestCtx, cancel := context.WithTimeout(ctx, g.timeout)
	defer cancel()

	prompt := geminiSystemPrompt +
		"\n\n[使用者]\n" + truncate(userText, 3000) +
		"\n\n[助手]\n" + truncate(assistantText, 3000)

	res, err := g.p.Generate(requestCtx, core.ModelRequest{
		Messages: []core.Message{{
			Role:  core.ROLE_USER,
			Parts: []core.Part{{Kind: core.PART_KIND_PLAIN_TEXT, Text: prompt}},
		}},
		// 96 was too tight: gemma sometimes prefaces its answer with a
		// "<thought>...</thought>" scratchpad (see stripThought below) before
		// the real sentence, and 96 tokens let it exhaust the budget mid-thought
		// with no answer ever produced. 800 leaves room for both.
		MaxTokens: 800,
	})
	if err != nil {
		if ctx.Err() != nil {
			return Result{}, ctx.Err()
		}
		if strings.Contains(strings.ToLower(err.Error()), "status 429") {
			return Result{}, fmt.Errorf("%w: %v", ErrRateLimited, err)
		}
		return g.fallback.Summarize(ctx, userText, assistantText)
	}
	summary := stripThought(strings.TrimSpace(res.Text))
	if summary == "" {
		return g.fallback.Summarize(ctx, userText, assistantText)
	}
	return Result{
		User:    truncate(userText, 120),
		Summary: truncate(firstLine(summary), 80),
		Source:  "llm",
	}, nil
}

// stripThought removes a leading "<thought>...</thought>" scratchpad block.
// Despite geminiSystemPrompt asking for a bare final sentence, gemma
// occasionally still emits a reasoning preamble in that literal tag before
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
