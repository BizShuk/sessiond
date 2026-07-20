package summarize

import (
	"context"
	"strings"
	"time"

	"github.com/bizshuk/agentsdk/core"
	"github.com/bizshuk/agentsdk/provider/google"
)

// DEFAULT_GEMMA_MODEL is the gemma id passed to the Google Generative Language
// API. Tentative — verify a currently-served id. Any invalid id makes Generate
// error, which degrades to the fallback summarizer, so a wrong default is safe.
const DEFAULT_GEMMA_MODEL = "gemma-4-26b-a4b-it"

const geminiSystemPrompt = `你是 AI coding session 的摘要器。把以下一個 turn(使用者指令 + 助手回應)濃縮成一句繁體中文摘要,聚焦「使用者想做什麼、結果如何」,不超過 200 字,不要引號、不要條列、不要前綴、不要輸出思考過程或 <thought> 標籤,直接給出最終的一句摘要。`

// Gemini summarizes via the agentSDK google provider (gemma model). On any
// error — no API key at call time, bad model id, timeout, empty output — it
// falls back to the injected summarizer so ingest never stalls or fails.
type Gemini struct {
	p        *google.Provider
	fallback Summarizer
	timeout  time.Duration
}

// NewGemini builds a Gemini backend. It returns an error when the provider can
// not be constructed (e.g. GOOGLE_API_KEY unset); callers then keep Heuristic.
//
// google.New no longer takes a context — the new signature is just
// `New(opts ...Option)`. ctx arg kept here only as a hook for callers that
// still pass it; it is intentionally unused. To be removed once call sites drop it.
func NewGemini(_ context.Context, model string, fallback Summarizer) (*Gemini, error) {
	if model == "" {
		model = DEFAULT_GEMMA_MODEL
	}
	p, err := google.New(google.WithModel(model))
	if err != nil {
		return nil, err
	}
	return &Gemini{p: p, fallback: fallback, timeout: 20 * time.Second}, nil
}

func (g *Gemini) Summarize(userText, assistantText string) Result {
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	prompt := geminiSystemPrompt +
		"\n\n[使用者]\n" + truncate(userText, 3000) +
		"\n\n[助手]\n" + truncate(assistantText, 3000)

	res, err := g.p.Generate(ctx, core.ModelRequest{
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
	summary := stripThought(strings.TrimSpace(res.Text))
	if err != nil || summary == "" {
		return g.fallback.Summarize(userText, assistantText)
	}
	return Result{
		User:    truncate(userText, 120),
		Summary: truncate(firstLine(summary), 80),
		Source:  "llm",
	}
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
