package scanner

import "sync"

// HealthTracker tracks consecutive provider failures independently per chain.
type HealthTracker struct {
	mu        sync.Mutex
	threshold int
	states    map[string]*healthState
}

type healthState struct {
	failures  int
	unhealthy bool
	alertSent bool
}

// NewHealthTracker creates a provider health tracker.
func NewHealthTracker(threshold int) *HealthTracker {
	return &HealthTracker{threshold: threshold, states: make(map[string]*healthState)}
}

// Failure records an error and reports whether the chain is now unhealthy.
func (h *HealthTracker) Failure(chain string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	state := h.state(chain)
	state.failures++
	if state.failures >= h.threshold {
		state.unhealthy = true
	}
	return state.unhealthy
}

// Success clears a chain's consecutive failures and reports recovery.
func (h *HealthTracker) Success(chain string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	state := h.state(chain)
	recovered := state.unhealthy
	state.failures = 0
	state.unhealthy = false
	state.alertSent = false
	return recovered
}

// IsUnhealthy reports whether a chain is currently paused by health policy.
func (h *HealthTracker) IsUnhealthy(chain string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.state(chain).unhealthy
}

// TakeAlert returns true once for each transition into an unhealthy state.
func (h *HealthTracker) TakeAlert(chain string) (bool, int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	state := h.state(chain)
	if !state.unhealthy || state.alertSent {
		return false, state.failures
	}
	state.alertSent = true
	return true, state.failures
}

func (h *HealthTracker) state(chain string) *healthState {
	state, ok := h.states[chain]
	if !ok {
		state = &healthState{}
		h.states[chain] = state
	}
	return state
}
