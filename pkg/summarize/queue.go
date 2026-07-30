package summarize

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// ErrRateLimited marks a provider response that should keep the current event
// at the head of the queue and retry it after RATE_LIMIT_PAUSE.
var ErrRateLimited = errors.New("summarizer rate limited")

// RATE_LIMIT_PAUSE is the fixed delay before retrying a rate-limited event.
const RATE_LIMIT_PAUSE = time.Minute

// Event is one user/assistant exchange waiting to be summarized.
type Event struct {
	UserText      string
	AssistantText string
}

type queue struct {
	summarizer Summarizer
	wait       func(context.Context, time.Duration) error
}

// ProcessQueue summarizes events in FIFO order. A rate-limited event remains
// at the head of the queue while processing pauses for one minute.
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
	pending := events
	results := make([]Result, 0, len(events))
	for len(pending) > 0 {
		event := pending[0]
		result, err := q.summarize(ctx, event)
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
	return q.summarizer.Summarize(ctx, event.UserText, event.AssistantText)
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
