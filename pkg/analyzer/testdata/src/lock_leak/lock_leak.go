package lock_leak

import "sync"

// --- Early return: Lock + early return without unlock → C5 ---

type EarlyReturn struct {
	mu    sync.Mutex
	value int
}

func (e *EarlyReturn) Set(v int) {
	e.mu.Lock()
	e.value = v
	e.mu.Unlock()
}

func (e *EarlyReturn) BadEarlyReturn(cond bool) error {
	e.mu.Lock()
	if cond {
		return nil // want `return without unlocking EarlyReturn\.mu \(locked at lock_leak\.go:\d+:\d+\)`
	}
	e.value = 42
	e.mu.Unlock()
	return nil
}

// --- Multiple early returns: Lock + several returns, only last has unlock → C5 on others ---

type MultiReturn struct {
	mu    sync.Mutex
	count int
}

func (m *MultiReturn) Set(v int) {
	m.mu.Lock()
	m.count = v
	m.mu.Unlock()
}

func (m *MultiReturn) BadMultiReturn(a, b bool) error {
	m.mu.Lock()
	if a {
		return nil // want `return without unlocking MultiReturn\.mu \(locked at lock_leak\.go:\d+:\d+\)`
	}
	if b {
		return nil // want `return without unlocking MultiReturn\.mu \(locked at lock_leak\.go:\d+:\d+\)`
	}
	m.count = 1
	m.mu.Unlock()
	return nil
}

// --- Defer safe: Lock + defer Unlock → no diagnostic ---

type DeferSafe struct {
	mu    sync.Mutex
	state int
}

func (d *DeferSafe) Set(v int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.state = v
}

func (d *DeferSafe) GetWithEarlyReturn(cond bool) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	if cond {
		return -1 // safe: defer covers it
	}
	return d.state
}

// --- Explicit unlock safe: Lock + Unlock + return → no diagnostic ---

type ExplicitSafe struct {
	mu   sync.Mutex
	data int
}

func (e *ExplicitSafe) Set(v int) {
	e.mu.Lock()
	e.data = v
	e.mu.Unlock()
}

// --- Conditional defer: defer only in one branch → C5 on the other ---

type ConditionalDefer struct {
	mu    sync.Mutex
	value int
}

func (c *ConditionalDefer) Set(v int) {
	c.mu.Lock()
	c.value = v
	c.mu.Unlock()
}

func (c *ConditionalDefer) BadConditionalDefer(cond bool) {
	c.mu.Lock()
	if cond {
		defer c.mu.Unlock()
		c.value = 1
		return // safe: defer covers this branch
	}
	return // want `return without unlocking ConditionalDefer\.mu \(locked at lock_leak\.go:\d+:\d+\)`
}

// --- RWMutex leak: RLock + return without RUnlock → C5 ---

type RWLeaker struct {
	mu    sync.RWMutex
	value int
}

func (r *RWLeaker) Set(v int) {
	r.mu.Lock()
	r.value = v
	r.mu.Unlock()
}

func (r *RWLeaker) Read() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.value
}

func (r *RWLeaker) BadRead(cond bool) int {
	r.mu.RLock()
	if cond {
		return -1 // want `return without unlocking RWLeaker\.mu \(locked at lock_leak\.go:\d+:\d+\)`
	}
	v := r.value
	r.mu.RUnlock()
	return v
}

// --- Suppressed with //mu:ignore on function ---

type IntentionalAcquire struct {
	mu    sync.Mutex
	value int
}

func (i *IntentionalAcquire) Set(v int) {
	i.mu.Lock()
	i.value = v
	i.mu.Unlock()
}

//mu:ignore
func (i *IntentionalAcquire) acquireAndReturn() {
	i.mu.Lock()
	return // intentional: caller will unlock
}
