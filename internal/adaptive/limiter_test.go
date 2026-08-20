package adaptive

import (
	"context"
	"testing"
	"time"
)

func TestLimiterEnforcesHardConcurrencyCap(t *testing.T) {
	limiter := New(Config{InitialConcurrency: 2, MaxConcurrency: 2, QueueWait: 5 * time.Millisecond})
	releaseOne, _, ok := limiter.Acquire(context.Background(), "ethereum")
	if !ok {
		t.Fatal("first acquire failed")
	}
	releaseTwo, _, ok := limiter.Acquire(context.Background(), "ethereum")
	if !ok {
		t.Fatal("second acquire failed")
	}
	if _, retryAfter, ok := limiter.Acquire(context.Background(), "ethereum"); ok || retryAfter <= 0 {
		t.Fatalf("expected capacity rejection, ok=%v retry_after=%s", ok, retryAfter)
	}
	releaseOne(Outcome{Chain: "ethereum", Latency: time.Millisecond})
	releaseTwo(Outcome{Chain: "ethereum", Latency: time.Millisecond})
	if got := limiter.Snapshot().ActiveInFlight; got != 0 {
		t.Fatalf("active requests = %d", got)
	}
}

func TestLimiterAdaptsUpAndDown(t *testing.T) {
	up := New(Config{InitialConcurrency: 2, MaxConcurrency: 10, AdjustInterval: time.Nanosecond, TargetLatency: time.Second})
	for i := 0; i < 10; i++ {
		release, _, ok := up.Acquire(context.Background(), "ethereum")
		if !ok {
			t.Fatal("increase acquire failed")
		}
		release(Outcome{Chain: "ethereum", Latency: time.Millisecond})
	}
	if got := up.Snapshot().RecommendedConcurrency; got <= 2 {
		t.Fatalf("expected increase, got %d", got)
	}

	down := New(Config{InitialConcurrency: 8, MaxConcurrency: 10, AdjustInterval: time.Nanosecond})
	for i := 0; i < 10; i++ {
		release, _, ok := down.Acquire(context.Background(), "ethereum")
		if !ok {
			t.Fatal("decrease acquire failed")
		}
		release(Outcome{Chain: "ethereum", Retryable: true, Latency: time.Millisecond})
	}
	if got := down.Snapshot().RecommendedConcurrency; got >= 8 {
		t.Fatalf("expected decrease, got %d", got)
	}
}
