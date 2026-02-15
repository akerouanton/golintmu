package basic

import "sync"

// --- Test 1: Simple struct with locked + unlocked access → diagnostic ---

type Counter struct {
	mu    sync.Mutex
	count int
}

func (c *Counter) Inc() {
	c.mu.Lock()
	c.count++ // locked write → triggers guard inference
	c.mu.Unlock()
}

func (c *Counter) Get() int {
	return c.count // want `field Counter\.count is accessed without holding Counter\.mu`
}

// --- Test 2: Field only accessed without lock → no guard inferred → no diagnostic ---

type Unguarded struct {
	mu   sync.Mutex
	name string
}

func (u *Unguarded) SetName(n string) {
	u.name = n
}

func (u *Unguarded) GetName() string {
	return u.name
}

// --- Test 3: All accesses are locked → no violation ---

type Safe struct {
	mu    sync.Mutex
	value int
}

func (s *Safe) Set(v int) {
	s.mu.Lock()
	s.value = v
	s.mu.Unlock()
}

func (s *Safe) Get() int {
	s.mu.Lock()
	v := s.value
	s.mu.Unlock()
	return v
}

// --- Test 4: Constructor accesses without lock → excluded ---

type Config struct {
	mu      sync.Mutex
	timeout int
}

func NewConfig(timeout int) *Config {
	c := &Config{}
	c.timeout = timeout // constructor — excluded from inference
	return c
}

func (c *Config) SetTimeout(t int) {
	c.mu.Lock()
	c.timeout = t // locked write → triggers guard inference
	c.mu.Unlock()
}

func (c *Config) GetTimeout() int {
	return c.timeout // want `field Config\.timeout is accessed without holding Config\.mu`
}

// --- Test 5: Immutable field — written in constructor, only read elsewhere ---

type Service struct {
	mu   sync.Mutex
	name string
	port int
}

func NewService(name string, port int) *Service {
	return &Service{name: name, port: port}
}

func (s *Service) Name() string {
	return s.name // no diagnostic: name is immutable
}

func (s *Service) Port() int {
	return s.port // want `field Service\.port is accessed without holding Service\.mu`
}

func (s *Service) SetPort(p int) {
	s.mu.Lock()
	s.port = p // this makes port NOT immutable — it's written outside constructor
	s.mu.Unlock()
}

func (s *Service) GetPort() int {
	return s.port // want `field Service\.port is accessed without holding Service\.mu`
}

// --- Test 6: Multiple methods, some lock some don't → diagnostic on unlocked ---

type Pool struct {
	mu      sync.Mutex
	workers int
}

func (p *Pool) AddWorker() {
	p.mu.Lock()
	p.workers++ // locked
	p.mu.Unlock()
}

func (p *Pool) RemoveWorker() {
	p.mu.Lock()
	p.workers-- // locked
	p.mu.Unlock()
}

func (p *Pool) WorkerCount() int {
	return p.workers // want `field Pool\.workers is accessed without holding Pool\.mu`
}
