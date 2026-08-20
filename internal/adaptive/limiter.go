package adaptive

import (
	"context"
	"sync"
	"time"
)

// Config controls the adaptive in-flight request limiter.
type Config struct {
	InitialConcurrency int
	MaxConcurrency     int
	QueueWait          time.Duration
	AdjustInterval     time.Duration
	TargetLatency      time.Duration
}

// Outcome describes one completed provider request.
type Outcome struct {
	Chain     string
	Retryable bool
	Latency   time.Duration
}

// ChainSnapshot describes the current recommendation for one chain.
type ChainSnapshot struct {
	RecommendedConcurrency int `json:"recommended_concurrency"`
	ActiveInFlight         int `json:"active_in_flight"`
}

// Snapshot is safe to expose to an internal capacity endpoint.
type Snapshot struct {
	MaxInFlight            int                      `json:"max_in_flight"`
	RecommendedConcurrency int                      `json:"recommended_concurrency"`
	ActiveInFlight         int                      `json:"active_in_flight"`
	QueueWaitMilliseconds  int                      `json:"queue_wait_ms"`
	Chains                 map[string]ChainSnapshot `json:"chains"`
}

type chainState struct {
	limit          int
	active         int
	samples        int
	failures       int
	latencyEWMA    time.Duration
	lastAdjustment time.Time
}

// Limiter adapts concurrency from request outcomes while enforcing a hard cap.
type Limiter struct {
	mu           sync.Mutex
	max          int
	globalLimit  int
	globalActive int
	queueWait    time.Duration
	interval     time.Duration
	target       time.Duration
	chains       map[string]*chainState
	global       chainState
}

// New creates an adaptive limiter. Initial values are clamped to the hard cap.
func New(config Config) *Limiter {
	if config.MaxConcurrency < 1 {
		config.MaxConcurrency = 1
	}
	if config.InitialConcurrency < 1 {
		config.InitialConcurrency = 1
	}
	if config.InitialConcurrency > config.MaxConcurrency {
		config.InitialConcurrency = config.MaxConcurrency
	}
	if config.QueueWait <= 0 {
		config.QueueWait = 250 * time.Millisecond
	}
	if config.AdjustInterval <= 0 {
		config.AdjustInterval = 5 * time.Second
	}
	if config.TargetLatency <= 0 {
		config.TargetLatency = 800 * time.Millisecond
	}
	now := time.Now()
	return &Limiter{
		max:         config.MaxConcurrency,
		globalLimit: config.InitialConcurrency,
		queueWait:   config.QueueWait,
		interval:    config.AdjustInterval,
		target:      config.TargetLatency,
		chains:      make(map[string]*chainState),
		global:      chainState{limit: config.InitialConcurrency, lastAdjustment: now},
	}
}

// Acquire reserves one global and one chain slot, waiting briefly for capacity.
// The boolean is false when the queue wait expires or the request is canceled.
func (l *Limiter) Acquire(ctx context.Context, chain string) (func(Outcome), time.Duration, bool) {
	deadline := time.Now().Add(l.queueWait)
	for {
		l.mu.Lock()
		state := l.chain(chain)
		if l.globalActive < l.globalLimit && state.active < state.limit {
			l.globalActive++
			state.active++
			l.mu.Unlock()
			return func(outcome Outcome) { l.release(outcome) }, 0, true
		}
		wait := time.Until(deadline)
		l.mu.Unlock()
		if wait <= 0 {
			return func(Outcome) {}, l.retryAfter(chain), false
		}
		if wait > 10*time.Millisecond {
			wait = 10 * time.Millisecond
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return func(Outcome) {}, l.retryAfter(chain), false
		case <-timer.C:
		}
	}
}

func (l *Limiter) release(outcome Outcome) {
	l.mu.Lock()
	defer l.mu.Unlock()
	state := l.chain(outcome.Chain)
	if l.globalActive > 0 {
		l.globalActive--
	}
	if state.active > 0 {
		state.active--
	}
	updateStats(&l.global, outcome)
	updateStats(state, outcome)
	l.adjust(&l.global, true)
	l.adjust(state, false)
}

func updateStats(state *chainState, outcome Outcome) {
	state.samples++
	if outcome.Retryable {
		state.failures++
	}
	if state.latencyEWMA == 0 {
		state.latencyEWMA = outcome.Latency
		return
	}
	state.latencyEWMA = (state.latencyEWMA*4 + outcome.Latency) / 5
}

func (l *Limiter) adjust(state *chainState, global bool) {
	if state.samples < 10 || time.Since(state.lastAdjustment) < l.interval {
		return
	}
	if state.failures > 0 || state.latencyEWMA > l.target*2 {
		state.limit /= 2
		if state.limit < 1 {
			state.limit = 1
		}
	} else if state.latencyEWMA <= l.target {
		increase := state.limit / 5
		if increase < 1 {
			increase = 1
		}
		state.limit += increase
		if state.limit > l.max {
			state.limit = l.max
		}
	}
	if global {
		l.globalLimit = state.limit
	}
	state.samples = 0
	state.failures = 0
	state.lastAdjustment = time.Now()
}

func (l *Limiter) chain(name string) *chainState {
	state, ok := l.chains[name]
	if !ok {
		state = &chainState{limit: l.globalLimit, lastAdjustment: time.Now()}
		l.chains[name] = state
	}
	return state
}

func (l *Limiter) retryAfter(chain string) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	state := l.chain(chain)
	if state.limit < 1 || l.globalLimit < 1 {
		return time.Second
	}
	return 250 * time.Millisecond
}

// Register makes a chain visible in capacity snapshots before its first request.
func (l *Limiter) Register(chain string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.chain(chain)
}

// Snapshot returns current global and per-chain capacity recommendations.
func (l *Limiter) Snapshot() Snapshot {
	l.mu.Lock()
	defer l.mu.Unlock()
	chains := make(map[string]ChainSnapshot, len(l.chains))
	for name, state := range l.chains {
		chains[name] = ChainSnapshot{RecommendedConcurrency: state.limit, ActiveInFlight: state.active}
	}
	return Snapshot{
		MaxInFlight:            l.max,
		RecommendedConcurrency: l.globalLimit,
		ActiveInFlight:         l.globalActive,
		QueueWaitMilliseconds:  int(l.queueWait / time.Millisecond),
		Chains:                 chains,
	}
}
