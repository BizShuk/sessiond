package summarize

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bizshuk/agentsdk/core"
)

type googleRequest struct {
	model         string
	authorization string
	err           error
}

type stubGenerator struct {
	result core.ModelResult
	err    error
}

func (s *stubGenerator) Generate(
	_ context.Context,
	_ core.ModelRequest,
) (core.ModelResult, error) {
	return s.result, s.err
}

func TestNewGeminiUsesAgentSDKRegistry(t *testing.T) {
	requests := make(chan googleRequest, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			requests <- googleRequest{err: err}
			http.Error(w, "decode request", http.StatusBadRequest)
			return
		}
		requests <- googleRequest{
			model:         body.Model,
			authorization: r.Header.Get("Authorization"),
		}
		w.Header().Set("Content-Type", "application/json")
		if _, err := w.Write([]byte(`{
			"choices": [
				{"message": {"role": "assistant", "content": "registry summary"}, "finish_reason": "stop"}
			]
		}`)); err != nil {
			return
		}
	}))
	t.Cleanup(server.Close)
	t.Setenv("GOOGLE_API_KEY", "test-key")
	t.Setenv("GOOGLE_BASE_URL", server.URL)

	g, err := NewGemini(context.Background(), "test-model", Heuristic{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := g.Summarize(context.Background(), "user", "assistant")
	if err != nil {
		t.Fatal(err)
	}

	request := <-requests
	if request.err != nil {
		t.Fatal(request.err)
	}
	if request.model != "test-model" {
		t.Fatalf("model = %q, want test-model", request.model)
	}
	if request.authorization != "Bearer test-key" {
		t.Fatalf("authorization = %q, want Bearer test-key", request.authorization)
	}
	if result.Summary != "registry summary" || result.Source != "llm" {
		t.Fatalf("result = %+v, want registry LLM summary", result)
	}
}

func TestNewGeminiRequiresGoogleAPIKey(t *testing.T) {
	t.Setenv("GOOGLE_API_KEY", "")

	_, err := NewGemini(context.Background(), "test-model", Heuristic{})

	if err == nil || !strings.Contains(err.Error(), "credential") {
		t.Fatalf("error = %v, want missing credential error", err)
	}
}

func TestGeminiSummarizeClassifiesRateLimit(t *testing.T) {
	g := &Gemini{
		p:        &stubGenerator{err: errors.New("google: status 429: quota exceeded")},
		fallback: Heuristic{},
		timeout:  time.Second,
	}

	_, err := g.Summarize(context.Background(), "user", "assistant")

	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("error = %v, want ErrRateLimited", err)
	}
}

func TestGeminiSummarizeFallsBackOnNonRateLimit(t *testing.T) {
	g := &Gemini{
		p:        &stubGenerator{err: errors.New("google: status 500: unavailable")},
		fallback: Heuristic{},
		timeout:  time.Second,
	}

	result, err := g.Summarize(context.Background(), "first line", "assistant")

	if err != nil {
		t.Fatal(err)
	}
	if result.Source != "heuristic" {
		t.Fatalf("source = %q, want heuristic", result.Source)
	}
}

func TestGeminiSummarizeHonorsParentCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	g := &Gemini{
		p:        &stubGenerator{err: context.Canceled},
		fallback: Heuristic{},
		timeout:  time.Second,
	}

	_, err := g.Summarize(ctx, "pending", "assistant")

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

// TestStripThought covers the gemma "<thought>...</thought>" scratchpad
// leakage reported against real ingest output: nearly every LLM summary was
// starting with raw reasoning text instead of the final sentence, because
// the model was cut off mid-thought before MaxTokens=96 ran out.
func TestStripThought(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "no thought tag passes through unchanged",
			in:   "使用者新增了 git hooks 安裝流程,已完成。",
			want: "使用者新增了 git hooks 安裝流程,已完成。",
		},
		{
			name: "closed thought block is dropped, trailing summary kept",
			in:   "<thought>* Role: AI coding session summarizer.</thought>使用者要求修 bug,已修好。",
			want: "使用者要求修 bug,已修好。",
		},
		{
			name: "closed thought block is case-insensitive",
			in:   "<THOUGHT>reasoning...</THOUGHT>結果如預期。",
			want: "結果如預期。",
		},
		{
			name: "unclosed thought block (truncated mid-reasoning) yields empty",
			in:   `<thought>*   Input: A single turn (User: "the hook input...`,
			want: "",
		},
		{
			name: "empty input stays empty",
			in:   "",
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := stripThought(tc.in); got != tc.want {
				t.Errorf("stripThought(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
