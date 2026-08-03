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

type llmRequest struct {
	model  string
	apiKey string
	err    error
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

// TestNewLLMUsesAgentSDKRegistry drives NewLLM against a real registered
// provider ("minimax") and a stub server. Switching providers in callers
// only requires changing the LLMOptions.Provider string; the constructor
// does not hardcode a specific registry name.
func TestNewLLMUsesAgentSDKRegistry(t *testing.T) {
	requests := make(chan llmRequest, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			requests <- llmRequest{err: err}
			http.Error(w, "decode request", http.StatusBadRequest)
			return
		}
		requests <- llmRequest{
			model:  body.Model,
			apiKey: r.Header.Get("x-api-key"),
		}
		w.Header().Set("Content-Type", "application/json")
		if _, err := w.Write([]byte(`{
			"id": "msg_test",
			"type": "message",
			"role": "assistant",
			"model": "test-model",
			"stop_reason": "end_turn",
			"content": [
				{"type": "text", "text": "registry summary"}
			]
		}`)); err != nil {
			return
		}
	}))
	t.Cleanup(server.Close)
	t.Setenv("MINIMAX_API_KEY", "test-key")
	t.Setenv("MINIMAX_BASE_URL", server.URL)

	g, err := NewLLM(LLMOptions{Provider: "minimax", Model: "test-model"}, Heuristic{})
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
	if request.apiKey != "test-key" {
		t.Fatalf("x-api-key = %q, want test-key", request.apiKey)
	}
	if result.Summary != "registry summary" || result.Source != "llm" {
		t.Fatalf("result = %+v, want registry LLM summary", result)
	}
}

// TestNewLLMUnknownProviderErrors verifies the constructor refuses an
// unregistered provider name rather than silently defaulting.
func TestNewLLMUnknownProviderErrors(t *testing.T) {
	_, err := NewLLM(LLMOptions{Provider: "definitely-not-a-provider", Model: "m"}, Heuristic{})

	if err == nil {
		t.Fatal("expected error for unknown provider, got nil")
	}
}

func TestNewLLMRequiresAPIKey(t *testing.T) {
	t.Setenv("MINIMAX_API_KEY", "")

	_, err := NewLLM(LLMOptions{Provider: "minimax", Model: "test-model"}, Heuristic{})

	if err == nil || !strings.Contains(err.Error(), "credential") {
		t.Fatalf("error = %v, want missing credential error", err)
	}
}

func TestLLMSummarizeClassifiesRateLimit(t *testing.T) {
	l := &LLM{
		p:        &stubGenerator{err: errors.New("minimax: status 429: quota exceeded")},
		fallback: Heuristic{},
		timeout:  time.Second,
	}

	_, err := l.Summarize(context.Background(), "user", "assistant")

	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("error = %v, want ErrRateLimited", err)
	}
}

func TestLLMSummarizeFallsBackOnNonRateLimit(t *testing.T) {
	l := &LLM{
		p:        &stubGenerator{err: errors.New("minimax: status 500: unavailable")},
		fallback: Heuristic{},
		timeout:  time.Second,
	}

	result, err := l.Summarize(context.Background(), "first line", "assistant")

	if err != nil {
		t.Fatal(err)
	}
	if result.Source != "heuristic" {
		t.Fatalf("source = %q, want heuristic", result.Source)
	}
}

func TestLLMSummarizeHonorsParentCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	l := &LLM{
		p:        &stubGenerator{err: context.Canceled},
		fallback: Heuristic{},
		timeout:  time.Second,
	}

	_, err := l.Summarize(ctx, "pending", "assistant")

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

// TestIsProviderConfigured exercises the env-var lookup helper that lets
// callers short-circuit in "auto" mode without hardcoding env var names.
func TestIsProviderConfigured(t *testing.T) {
	t.Run("unknown provider is not configured", func(t *testing.T) {
		if IsProviderConfigured("not-a-real-provider") {
			t.Fatal("expected unknown provider to be unconfigured")
		}
	})

	t.Run("provider with key set is configured", func(t *testing.T) {
		t.Setenv("MINIMAX_API_KEY", "test-key")
		if !IsProviderConfigured("minimax") {
			t.Fatal("expected minimax to be configured when MINIMAX_API_KEY is set")
		}
	})

	t.Run("provider with empty key is not configured", func(t *testing.T) {
		t.Setenv("MINIMAX_API_KEY", "")
		if IsProviderConfigured("minimax") {
			t.Fatal("expected minimax to be unconfigured when MINIMAX_API_KEY is empty")
		}
	})
}

// TestStripThought covers the "<thought>...</thought>" scratchpad leakage
// reported against real ingest output: nearly every LLM summary was starting
// with raw reasoning text instead of the final sentence, because the model
// was cut off mid-thought before MaxTokens=96 ran out.
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
