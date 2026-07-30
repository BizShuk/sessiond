package summarize

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

type scriptedStep struct {
	result Result
	err    error
}

type scriptedSummarizer struct {
	steps []scriptedStep
	users []string
}

func (s *scriptedSummarizer) Summarize(
	_ context.Context,
	userText string,
	_ string,
) (Result, error) {
	s.users = append(s.users, userText)
	if len(s.steps) == 0 {
		return Result{}, errors.New("scripted summarizer has no steps")
	}
	step := s.steps[0]
	s.steps = s.steps[1:]
	return step.result, step.err
}

func TestQueueRateLimitWaitsOneMinuteAndRetriesHead(t *testing.T) {
	s := &scriptedSummarizer{
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
	s := &scriptedSummarizer{steps: []scriptedStep{{err: ErrRateLimited}}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := ProcessQueue(ctx, s, []Event{{UserText: "pending"}})

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}
