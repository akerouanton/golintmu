package interprocedural_verbose

import "sync"

// --- Direct requirement: single field access without lock ---

type Counter struct {
	mu    sync.Mutex
	count int
}

// Read establishes the guard: count is protected by mu.
func (c *Counter) Read() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.count
}

// increment accesses count without holding mu — requires mu from callers.
func (c *Counter) increment() {
	c.count++ // (suppressed: requirement propagated to call sites)
}

// SafeIncrement holds the lock — no violation.
func (c *Counter) SafeIncrement() {
	c.mu.Lock()
	c.increment()
	c.mu.Unlock()
}

// UnsafeIncrement doesn't hold mu — violation with direct provenance.
func (c *Counter) UnsafeIncrement() {
	c.increment() // want `Counter\.mu must be held when calling increment\(\)\n\tincrement\(\) accesses Counter\.count at verbose\.go:\d+:\d+`
}

// --- Transitive requirement: 2-hop call chain ---

type Service struct {
	mu   sync.Mutex
	data string
}

// GetData establishes the guard: data is protected by mu.
func (s *Service) GetData() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.data
}

// innerHelper accesses data without holding mu.
func (s *Service) innerHelper() {
	s.data = "updated" // (suppressed: requirement propagated)
}

// middleHelper calls innerHelper — transitively requires mu.
func (s *Service) middleHelper() {
	s.innerHelper() // (suppressed: requirement propagated)
}

// SafeDeepCall holds mu — no violation.
func (s *Service) SafeDeepCall() {
	s.mu.Lock()
	s.middleHelper()
	s.mu.Unlock()
}

// UnsafeDeepCall doesn't hold mu — violation with transitive provenance.
func (s *Service) UnsafeDeepCall() {
	s.middleHelper() // want `Service\.mu must be held when calling middleHelper\(\)\n\tmiddleHelper\(\) calls innerHelper\(\) at verbose\.go:\d+:\d+\n\tinnerHelper\(\) accesses Service\.data at verbose\.go:\d+:\d+`
}

// --- Multiple direct accesses (verify cap at 3 chains) ---

type Multi struct {
	mu sync.Mutex
	a  int
	b  int
	c  int
	d  int
}

// Read establishes the guard: a, b, c, d are all protected by mu.
func (m *Multi) Read() (int, int, int, int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.a, m.b, m.c, m.d
}

// touchAll accesses a, b, c, d without holding mu.
func (m *Multi) touchAll() {
	m.a = 1
	m.b = 2
	m.c = 3
	m.d = 4
}

// UnsafeMulti doesn't hold mu — violation with multiple provenance chains (capped at 3).
// We verify the diagnostic contains at least two provenance lines separated by a blank line.
func (m *Multi) UnsafeMulti() {
	m.touchAll() // want `Multi\.mu must be held when calling touchAll\(\)\n\ttouchAll\(\) accesses Multi\.\w+ at verbose\.go:\d+:\d+\n\n\ttouchAll\(\) accesses Multi\.\w+ at verbose\.go:\d+:\d+`
}
