package interprocedural

import "sync"

// --- Helper called with and without lock (requirement propagation) ---

type Counter struct {
	mu    sync.Mutex
	count int
}

// Read accesses count with mu held, establishing the guard.
func (c *Counter) Read() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.count
}

// increment is a helper that accesses count without holding mu.
// It should require mu to be held by callers.
func (c *Counter) increment() {
	c.count++ // (suppressed: requirement propagated to call sites)
}

// SafeIncrement calls increment with mu held — no violation.
func (c *Counter) SafeIncrement() {
	c.mu.Lock()
	c.increment()
	c.mu.Unlock()
}

// UnsafeIncrement calls increment without mu held — violation at call site.
func (c *Counter) UnsafeIncrement() {
	c.increment() // want `Counter\.mu must be held when calling increment\(\)`
}

// --- Deep call chain (transitive requirement propagation) ---

type Service struct {
	mu   sync.Mutex
	data string
}

// GetData accesses data with mu held, establishing the guard.
func (s *Service) GetData() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.data
}

// innerHelper accesses data without holding mu.
func (s *Service) innerHelper() {
	s.data = "updated" // (suppressed: requirement propagated)
}

// middleHelper calls innerHelper, transitively requires mu.
func (s *Service) middleHelper() {
	s.innerHelper() // (suppressed: requirement propagated)
}

// SafeDeepCall holds mu and calls through the chain — no violation.
func (s *Service) SafeDeepCall() {
	s.mu.Lock()
	s.middleHelper()
	s.mu.Unlock()
}

// UnsafeDeepCall doesn't hold mu — violation at call site.
func (s *Service) UnsafeDeepCall() {
	s.middleHelper() // want `Service\.mu must be held when calling middleHelper\(\)`
}

// --- Constructor exclusion still works ---

type Cache struct {
	mu    sync.Mutex
	items map[string]string
}

func NewCache() *Cache {
	c := &Cache{
		items: make(map[string]string), // constructor: no violation
	}
	return c
}

func (c *Cache) Get(key string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.items[key]
}

func (c *Cache) Set(key, value string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items[key] = value
}
