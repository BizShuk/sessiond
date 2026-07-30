# Rate Limit Event Queue Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use `executing-plans` to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 讓新增的 LLM summary events 經由 FIFO queue 依序處理；遇到 HTTP 429 rate limit 時保留 queue head、暫停 1 分鐘後重試。

**Architecture:** `pkg/summarize/queue.go` 擁有 queue 與 retry pause policy，`pkg/summarize/gemini.go` 將 Google provider 的 `status 429` error 分類為 sentinel error，`pkg/hook/hook.go` 將同一 hook 中尚未持久化的 turns 交給 queue。Queue 只在成功取得 LLM 結果或非 rate-limit fallback 後移除 event；等待被 context 取消時整批不寫入，下一次 hook 會依 transcript 與 store count 重新取得尚未完成的 turns。

**Tech Stack:** Go 1.26、stdlib `context`/`errors`/`time`、既有 `agentsdk` Google provider、Go `testing`

## Global Constraints

- Rate-limit pause 固定為 `1 * time.Minute`，不新增 runtime configuration。
- FIFO queue 不讓後續 event 越過被 rate-limit 的 queue head。
- 非 rate-limit provider error 維持既有 heuristic fallback。
- Hook 對 Claude/Codex 仍維持 best-effort 與 stdout contract。
- 不新增 background goroutine；hook 是短生命週期 process，使用同步 slice queue 才能確保 process 結束前完成或明確取消。
- 不修改不相關的 pause/resume、install、ingest、store schema。

---

### Task 1: FIFO Queue 與固定 pause policy

**Files:**

- Create: `pkg/summarize/queue.go`
- Create: `pkg/summarize/queue_test.go`

**Interfaces:**

- Consumes: 現有 `Summarizer.Summarize(userText, assistantText string) Result`
- Produces: `Event`、`ErrRateLimited`、`ProcessQueue(context.Context, Summarizer, []Event) ([]Result, error)`
- Internal extension seam: `SummarizeContext(context.Context, string, string) (Result, error)`

- [x] **Step 1: Write failing FIFO/rate-limit tests**

```go
type scriptedStep struct {
	result Result
	err    error
}

type scriptedContextSummarizer struct {
	steps []scriptedStep
	users []string
}

func (s *scriptedContextSummarizer) Summarize(userText, assistantText string) Result {
	return Heuristic{}.Summarize(userText, assistantText)
}

func (s *scriptedContextSummarizer) SummarizeContext(
	_ context.Context,
	userText string,
	_ string,
) (Result, error) {
	s.users = append(s.users, userText)
	step := s.steps[0]
	s.steps = s.steps[1:]
	return step.result, step.err
}

func TestQueueRateLimitWaitsOneMinuteAndRetriesHead(t *testing.T) {
	s := &scriptedContextSummarizer{
		steps: []scriptedStep{
			{err: ErrRateLimited},
			{result: Result{Summary: "first"}},
			{result: Result{Summary: "second"}},
		},
	}
	var waits []time.Duration
	q := newQueue(s, func(_ context.Context, d time.Duration) error {
		waits = append(waits, d)
		return nil
	})

	results, err := q.process(context.Background(), []Event{
		{UserText: "first"},
		{UserText: "second"},
	})

	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"first", "first", "second"}; !reflect.DeepEqual(s.users, want) {
		t.Fatalf("call order = %v, want %v", s.users, want)
	}
	if want := []time.Duration{time.Minute}; !reflect.DeepEqual(waits, want) {
		t.Fatalf("waits = %v, want %v", waits, want)
	}
	if want := []Result{{Summary: "first"}, {Summary: "second"}}; !reflect.DeepEqual(results, want) {
		t.Fatalf("results = %#v, want %#v", results, want)
	}
}

func TestQueueReturnsWhenRateLimitWaitIsCancelled(t *testing.T) {
	s := &scriptedContextSummarizer{steps: []scriptedStep{{err: ErrRateLimited}}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := ProcessQueue(ctx, s, []Event{{UserText: "pending"}})

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}
```

- [x] **Step 2: Run RED tests**

Run: `go test -count=1 ./pkg/summarize -run 'TestQueue'`

Expected: FAIL because `Event`、`ErrRateLimited`、`newQueue`、`ProcessQueue` 尚未定義。

- [x] **Step 3: Implement minimal synchronous queue**

```go
var ErrRateLimited = errors.New("summarizer rate limited")

const RATE_LIMIT_PAUSE = time.Minute

type Event struct {
	UserText      string
	AssistantText string
}

type contextSummarizer interface {
	SummarizeContext(context.Context, string, string) (Result, error)
}

type queue struct {
	summarizer Summarizer
	wait       func(context.Context, time.Duration) error
}

func ProcessQueue(ctx context.Context, summarizer Summarizer, events []Event) ([]Result, error) {
	return newQueue(summarizer, waitContext).process(ctx, events)
}

func newQueue(
	summarizer Summarizer,
	wait func(context.Context, time.Duration) error,
) *queue {
	return &queue{summarizer: summarizer, wait: wait}
}

func (q *queue) process(ctx context.Context, events []Event) ([]Result, error) {
	pending := append([]Event(nil), events...)
	results := make([]Result, 0, len(pending))
	for len(pending) > 0 {
		result, err := q.summarize(ctx, pending[0])
		if errors.Is(err, ErrRateLimited) {
			if err := q.wait(ctx, RATE_LIMIT_PAUSE); err != nil {
				return nil, fmt.Errorf("wait after rate limit: %w", err)
			}
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("summarize queued event: %w", err)
		}
		results = append(results, result)
		pending = pending[1:]
	}
	return results, nil
}

func (q *queue) summarize(ctx context.Context, event Event) (Result, error) {
	if summarizer, ok := q.summarizer.(contextSummarizer); ok {
		return summarizer.SummarizeContext(ctx, event.UserText, event.AssistantText)
	}
	return q.summarizer.Summarize(event.UserText, event.AssistantText), nil
}

func waitContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
```

- [x] **Step 4: Run GREEN tests**

Run: `go test -count=1 ./pkg/summarize -run 'TestQueue'`

Expected: PASS。

### Task 2: Gemini 429 classification 與原有 fallback

**Files:**

- Modify: `pkg/summarize/gemini.go`
- Modify: `pkg/summarize/gemini_test.go`

**Interfaces:**

- Consumes: `ErrRateLimited` from Task 1
- Produces: `Gemini.SummarizeContext(context.Context, string, string) (Result, error)`
- Internal dependency: narrow `generator.Generate(context.Context, core.ModelRequest) (core.ModelResult, error)`

- [x] **Step 1: Write failing Gemini classification tests**

```go
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

func TestGeminiSummarizeContextClassifiesRateLimit(t *testing.T) {
	g := &Gemini{
		p:        &stubGenerator{err: errors.New("google: status 429: quota exceeded")},
		fallback: Heuristic{},
		timeout:  time.Second,
	}

	_, err := g.SummarizeContext(context.Background(), "user", "assistant")

	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("error = %v, want ErrRateLimited", err)
	}
}

func TestGeminiSummarizeContextFallsBackOnNonRateLimit(t *testing.T) {
	g := &Gemini{
		p:        &stubGenerator{err: errors.New("google: status 500: unavailable")},
		fallback: Heuristic{},
		timeout:  time.Second,
	}

	result, err := g.SummarizeContext(context.Background(), "first line", "assistant")

	if err != nil {
		t.Fatal(err)
	}
	if result.Source != "heuristic" {
		t.Fatalf("source = %q, want heuristic", result.Source)
	}
}
```

- [x] **Step 2: Run RED tests**

Run: `go test -count=1 ./pkg/summarize -run 'TestGeminiSummarizeContext'`

Expected: FAIL because `Gemini.p` 仍是 concrete provider，且 `SummarizeContext` 尚未定義。

- [x] **Step 3: Implement provider seam and classification**

```go
type generator interface {
	Generate(context.Context, core.ModelRequest) (core.ModelResult, error)
}

func (g *Gemini) SummarizeContext(ctx context.Context, userText, assistantText string) (Result, error) {
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
		MaxTokens: 800,
	})
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "status 429") {
			return Result{}, fmt.Errorf("%w: %v", ErrRateLimited, err)
		}
		return g.fallback.Summarize(userText, assistantText), nil
	}
	summary := stripThought(strings.TrimSpace(res.Text))
	if summary == "" {
		return g.fallback.Summarize(userText, assistantText), nil
	}
	return Result{
		User:    truncate(userText, 120),
		Summary: truncate(firstLine(summary), 80),
		Source:  "llm",
	}, nil
}
```

`Summarize` 保留既有 interface；直接呼叫時若收到 `ErrRateLimited` 仍使用 fallback。Queue path 則呼叫 `SummarizeContext` 並執行 1 分鐘 pause。

- [x] **Step 4: Run GREEN and existing thought-cleaning tests**

Run: `go test -count=1 ./pkg/summarize`

Expected: PASS。

### Task 3: Hook integration、documentation 與 completion audit

**Files:**

- Modify: `pkg/hook/hook.go`
- Modify: `pkg/hook/hook_test.go`
- Modify: `cmd/hook.go`
- Modify: `README.md`
- Modify: `README.todo`

**Interfaces:**

- Consumes: `summarize.ProcessQueue` and `summarize.Event`
- Produces: `RunContext(context.Context, RunOptions) error`
- Compatibility: `Run(RunOptions) error` delegates to `RunContext(context.Background(), opts)`

- [x] **Step 1: Write failing hook queue integration test**

```go
type rateLimitedSummarizer struct{}

func (rateLimitedSummarizer) Summarize(userText, assistantText string) summarize.Result {
	return summarize.Heuristic{}.Summarize(userText, assistantText)
}

func (rateLimitedSummarizer) SummarizeContext(
	_ context.Context,
	userText string,
	assistantText string,
) (summarize.Result, error) {
	return summarize.Result{}, summarize.ErrRateLimited
}

func TestBuildTurnsRateLimitedQueueHonorsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	raws := []ingest.RawTurn{{
		UserText:      "pending",
		AssistantText: "answer",
	}}

	_, err := buildTurns(ctx, rateLimitedSummarizer{}, raws, hookpayload.Payload{}, 0)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}
```

Queue package tests 證明 wait duration 與 FIFO；這個 hook test 證明 hook 將 queue cancellation 傳回、且不會把 fallback turn 誤寫入。

- [x] **Step 2: Run RED hook test**

Run: `go test -count=1 ./pkg/hook -run 'TestBuildTurnsRateLimitedQueueHonorsContextCancellation'`

Expected: FAIL because `buildTurns` 尚未接受 context/queue results。

- [x] **Step 3: Integrate queue with context-aware hook execution**

```go
func Run(opts RunOptions) error {
	return RunContext(context.Background(), opts)
}

func buildTurns(
	ctx context.Context,
	s summarize.Summarizer,
	raws []ingest.RawTurn,
	p hookpayload.Payload,
	existing int,
) ([]model.Turn, error) {
	events := make([]summarize.Event, 0, len(raws)-existing)
	for i := existing; i < len(raws); i++ {
		events = append(events, summarize.Event{
			UserText:      raws[i].UserText,
			AssistantText: raws[i].AssistantText,
		})
	}
	results, err := summarize.ProcessQueue(ctx, s, events)
	if err != nil {
		return nil, err
	}
	return turnsFromResults(raws, results, p, existing), nil
}

func turnsFromResults(
	raws []ingest.RawTurn,
	results []summarize.Result,
	p hookpayload.Payload,
	existing int,
) []model.Turn {
	out := make([]model.Turn, 0, len(results))
	for offset, res := range results {
		i := existing + offset
		r := raws[i]
		status := "ok"
		turnID := ""
		if i == len(raws)-1 {
			turnID = p.TurnID
			if p.HookEventName == "StopFailure" {
				status = "error"
			}
		}
		at := r.At
		if at == "" {
			at = time.Now().UTC().Format(time.RFC3339)
		}
		out = append(out, model.Turn{
			Index:      i + 1,
			TurnID:     turnID,
			Event:      p.HookEventName,
			User:       res.User,
			Summary:    res.Summary,
			Source:     res.Source,
			Status:     status,
			At:         at,
			TokenCount: r.TokenCount,
		})
	}
	return out
}
```

`RunContext` 取代原本 `Run` 的 body，並將原本的 `buildTurns(...)` 呼叫改為：

```go
turns, err := buildTurns(ctx, summarizer, raws, p, existing)
if err != nil {
	log.Warn(
		"summarize queued turns failed",
		"agent", opts.Agent,
		"session_id", p.SessionID,
		"err", err,
	)
	return nil
}
```

`cmd/hook.go` 精確改用：

```go
hook.RunContext(cmd.Context(), hook.RunOptions{Agent: agent})
```

讓 process shutdown 可以取消 1 分鐘等待。

- [x] **Step 4: Update canonical user-facing state**

- `README.md` 說明 LLM events 使用 FIFO queue，429 時固定 pause 1 分鐘並重試，非 rate-limit error 才 fallback。
- `README.todo` 移除已完成的 `Queue LLM event` pending item。

- [x] **Step 5: Run formatting and targeted tests**

Run: `gofmt -w pkg/summarize/queue.go pkg/summarize/queue_test.go pkg/summarize/gemini.go pkg/summarize/gemini_test.go pkg/hook/hook.go pkg/hook/hook_test.go cmd/hook.go`

Run: `go test -count=1 ./pkg/summarize ./pkg/hook ./cmd`

Expected: PASS。

- [x] **Step 6: Run full verification and audit**

Run: `go test -count=1 ./...`

Run: `go test -race -count=1 ./...`

Run: `go vet ./...`

Run: `go build ./...`

Expected: 所有 commands exit 0；diff 只包含 queue、Gemini integration、hook context、README/TODO 與本 plan。
