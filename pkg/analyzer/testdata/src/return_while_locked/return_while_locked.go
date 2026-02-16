package return_while_locked

import "sync"

// --- Case 1: Acquire helper — locks and returns on all paths → callee-side diagnostic ---

type Registry struct {
	mu    sync.Mutex
	items map[string]int
}

func (r *Registry) Set(k string, v int) {
	r.mu.Lock()
	r.items[k] = v
	r.mu.Unlock()
}

func (r *Registry) lockAndGet(key string) int { // want `lockAndGet\(\) returns while holding Registry\.mu -- callers must unlock`
	r.mu.Lock()
	return r.items[key]
}

// --- Case 2: Unaware caller — calls acquire helper, never unlocks → caller-side diagnostic ---

func (r *Registry) BadCaller(key string) int {
	v := r.lockAndGet(key) // want `BadCaller\(\) calls lockAndGet\(\) which acquires Registry\.mu, but BadCaller\(\) never releases it`
	return v
}

// --- Case 3: Correct caller (explicit unlock) — no diagnostic ---

func (r *Registry) GoodCallerExplicit(key string) int {
	v := r.lockAndGet(key)
	r.mu.Unlock()
	return v
}

// --- Case 4: Correct caller (defer unlock) — no diagnostic ---

func (r *Registry) GoodCallerDefer(key string) int {
	defer r.mu.Unlock()
	v := r.lockAndGet(key)
	return v
}

// --- Case 5: Propagating acquire helper — helper A calls helper B ---
// outerAcquire calls innerAcquire but never unlocks → caller-side C13 on outerAcquire.
// outerAcquire itself is NOT an acquire helper (no local lock at return).

type Chain struct {
	mu   sync.Mutex
	data int
}

func (c *Chain) Set(v int) {
	c.mu.Lock()
	c.data = v
	c.mu.Unlock()
}

func (c *Chain) innerAcquire() int { // want `innerAcquire\(\) returns while holding Chain\.mu -- callers must unlock`
	c.mu.Lock()
	return c.data
}

func (c *Chain) outerAcquire() int {
	return c.innerAcquire() // want `outerAcquire\(\) calls innerAcquire\(\) which acquires Chain\.mu, but outerAcquire\(\) never releases it`
}

// --- Case 6: Suppressed callee — //mu:ignore on acquire helper → no callee-side diagnostic ---

type Suppressed struct {
	mu    sync.Mutex
	value int
}

func (s *Suppressed) Set(v int) {
	s.mu.Lock()
	s.value = v
	s.mu.Unlock()
}

//mu:ignore
func (s *Suppressed) suppressedAcquire() int {
	s.mu.Lock()
	return s.value
}

// Caller-side diagnostic still fires even though callee is suppressed.
func (s *Suppressed) CallerOfSuppressed() int {
	v := s.suppressedAcquire() // want `CallerOfSuppressed\(\) calls suppressedAcquire\(\) which acquires Suppressed\.mu, but CallerOfSuppressed\(\) never releases it`
	return v
}

// --- Case 7: Suppressed caller — //mu:ignore on caller → no caller-side diagnostic ---

//mu:ignore
func (s *Suppressed) SuppressedCaller() int {
	v := s.suppressedAcquire()
	return v
}

// --- Case 8: Suppressed call line — //mu:nolint on line before call → no caller-side diagnostic ---

func (s *Suppressed) NolintCaller() int {
	//mu:nolint
	v := s.suppressedAcquire()
	return v
}

// --- Case 9: Partial-path return — lock held on some paths but not all → C5 not C13 ---

type PartialReturn struct {
	mu    sync.Mutex
	value int
}

func (p *PartialReturn) Set(v int) {
	p.mu.Lock()
	p.value = v
	p.mu.Unlock()
}

func (p *PartialReturn) partialLeak(cond bool) int {
	p.mu.Lock()
	if cond {
		return p.value // want `return without unlocking PartialReturn\.mu \(locked at return_while_locked\.go:\d+:\d+\)`
	}
	p.value = 42
	p.mu.Unlock()
	return 0
}

// --- Case 10: RWMutex acquire helper — RLock + return on all paths → C13 ---

type RWRegistry struct {
	mu    sync.RWMutex
	items map[string]int
}

func (r *RWRegistry) Set(k string, v int) {
	r.mu.Lock()
	r.items[k] = v
	r.mu.Unlock()
}

func (r *RWRegistry) Read() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.items["x"]
}

func (r *RWRegistry) rLockAndGet(key string) int { // want `rLockAndGet\(\) returns while holding RWRegistry\.mu -- callers must unlock`
	r.mu.RLock()
	return r.items[key]
}

func (r *RWRegistry) BadRWCaller(key string) int {
	v := r.rLockAndGet(key) // want `BadRWCaller\(\) calls rLockAndGet\(\) which acquires RWRegistry\.mu, but BadRWCaller\(\) never releases it`
	return v
}

func (r *RWRegistry) GoodRWCaller(key string) int {
	v := r.rLockAndGet(key)
	r.mu.RUnlock()
	return v
}
