package annotations

import "sync"

// --- Struct used across tests ---

type Counter struct {
	mu    sync.Mutex
	count int
}

// lockedInc always acquires the lock — establishes the guard.
func (c *Counter) lockedInc() {
	c.mu.Lock()
	c.count++
	c.mu.Unlock()
}

// --- //mu:concurrent: marks a function as concurrent entrypoint ---

//mu:concurrent
func plainConcurrent(c *Counter) {
	_ = c.count // want `field Counter\.count is accessed without holding Counter\.mu`
}

// --- //mu:ignore: suppresses all diagnostics in a function ---

//mu:ignore
func ignoredFunc(c *Counter) {
	_ = c.count // no diagnostic: function is ignored
}

// --- //mu:nolint: suppresses diagnostic on the next line only ---

func nolintLine(c *Counter) {
	//mu:nolint
	_ = c.count // no diagnostic: suppressed by nolint
}

// --- //mu:nolint does NOT suppress lines beyond the next one ---

//mu:concurrent
func nolintLimited(c *Counter) {
	//mu:nolint
	_ = c.count // no diagnostic: suppressed by nolint
	_ = c.count // want `field Counter\.count is accessed without holding Counter\.mu`
}

// --- Non-annotated function still reports normally ---

//mu:concurrent
func normalViolation(c *Counter) {
	_ = c.count // want `field Counter\.count is accessed without holding Counter\.mu`
}
