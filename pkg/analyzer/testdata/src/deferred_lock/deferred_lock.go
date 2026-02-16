package deferred_lock

import "sync"

// --- Struct with sync.Mutex ---

type SafeCounter struct {
	mu    sync.Mutex
	count int
}

// Set establishes the guard inference (mu guards count).
func (s *SafeCounter) Set(v int) {
	s.mu.Lock()
	s.count = v
	s.mu.Unlock()
}

// CorrectDefer: Lock + defer Unlock → no diagnostic.
func (s *SafeCounter) CorrectDefer() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.count
}

// VariantA: Lock + defer Lock → C7 diagnostic.
func (s *SafeCounter) VariantA() int {
	s.mu.Lock()
	defer s.mu.Lock() // want `defer SafeCounter\.mu\.Lock\(\) will deadlock — did you mean defer SafeCounter\.mu\.Unlock\(\)\?`
	return s.count
}

// VariantB: bare defer Lock (no prior lock) → C7 diagnostic + C1 (field unguarded).
func (s *SafeCounter) VariantB() int {
	defer s.mu.Lock() // want `defer SafeCounter\.mu\.Lock\(\) will deadlock — did you mean defer SafeCounter\.mu\.Unlock\(\)\?`
	return s.count    // want `field SafeCounter\.count is accessed without holding SafeCounter\.mu`
}

// --- Struct with sync.RWMutex ---

type RWCounter struct {
	rw    sync.RWMutex
	value int
}

// Set establishes the guard inference (rw guards value).
func (r *RWCounter) Set(v int) {
	r.rw.Lock()
	r.value = v
	r.rw.Unlock()
}

// Read establishes read-lock pattern.
func (r *RWCounter) Read() int {
	r.rw.RLock()
	defer r.rw.RUnlock()
	return r.value
}

// DeferRLock: RLock + defer RLock → C7 with RUnlock suggestion.
func (r *RWCounter) DeferRLock() int {
	r.rw.RLock()
	defer r.rw.RLock() // want `defer RWCounter\.rw\.RLock\(\) will deadlock — did you mean defer RWCounter\.rw\.RUnlock\(\)\?`
	return r.value
}

// DeferLock: Lock + defer Lock → C7 with Unlock suggestion.
func (r *RWCounter) DeferLock() int {
	r.rw.Lock()
	defer r.rw.Lock() // want `defer RWCounter\.rw\.Lock\(\) will deadlock — did you mean defer RWCounter\.rw\.Unlock\(\)\?`
	return r.value
}

// --- Suppression via annotations ---

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
func (s *Suppressed) IgnoredFunc() int {
	s.mu.Lock()
	defer s.mu.Lock() // suppressed by //mu:ignore on function
	return s.value
}

func (s *Suppressed) NolintLine() int {
	s.mu.Lock()
	//mu:nolint
	defer s.mu.Lock() // suppressed by //mu:nolint on preceding line
	return s.value
}
