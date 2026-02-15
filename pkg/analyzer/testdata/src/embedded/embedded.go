package embedded

import "sync"

// --- Test 1: Basic embedded Mutex — guarded + unguarded access ---

type Counter struct {
	sync.Mutex
	count int
}

func (c *Counter) Inc() {
	c.Lock()
	c.count++ // locked write → triggers guard inference
	c.Unlock()
}

func (c *Counter) Get() int {
	return c.count // want `field Counter\.count is accessed without holding Counter\.Mutex`
}

// --- Test 2: Embedded RWMutex — Lock/RLock patterns ---

type Cache struct {
	sync.RWMutex
	size int
}

func (c *Cache) Add() {
	c.Lock()
	c.size++ // exclusive write
	c.Unlock()
}

func (c *Cache) Size() int {
	c.RLock()
	v := c.size // read-locked read
	c.RUnlock()
	return v
}

func (c *Cache) UnsafeSize() int {
	return c.size // want `field Cache\.size is accessed without holding Cache\.RWMutex`
}

// --- Test 3: All locked — no violations ---

type Safe struct {
	sync.Mutex
	value int
}

func (s *Safe) Set(v int) {
	s.Lock()
	s.value = v
	s.Unlock()
}

func (s *Safe) Get() int {
	s.Lock()
	v := s.value
	s.Unlock()
	return v
}

// --- Test 4: Constructor exclusion with embedded mutex ---

type Config struct {
	sync.Mutex
	timeout int
}

func NewConfig(timeout int) *Config {
	c := &Config{}
	c.timeout = timeout // constructor — excluded from inference
	return c
}

func (c *Config) SetTimeout(t int) {
	c.Lock()
	c.timeout = t // locked write → triggers guard inference
	c.Unlock()
}

func (c *Config) GetTimeout() int {
	return c.timeout // want `field Config\.timeout is accessed without holding Config\.Mutex`
}

// --- Test 5: Defer pattern with embedded mutex ---

type DeferExample struct {
	sync.Mutex
	data string
}

func (d *DeferExample) SafeSet(s string) {
	d.Lock()
	defer d.Unlock()
	d.data = s // locked write
}

func (d *DeferExample) UnsafeGet() string {
	return d.data // want `field DeferExample\.data is accessed without holding DeferExample\.Mutex`
}

// --- Test 6: Mixed — embedded mutex + named mutex guarding different fields ---

type Mixed struct {
	sync.Mutex
	namedMu    sync.Mutex
	embField   int
	namedField int
}

func (m *Mixed) SetEmb(v int) {
	m.Lock()
	m.embField = v // locked by embedded Mutex
	m.Unlock()
}

func (m *Mixed) SetNamed(v int) {
	m.namedMu.Lock()
	m.namedField = v // locked by namedMu
	m.namedMu.Unlock()
}

func (m *Mixed) GetEmb() int {
	return m.embField // want `field Mixed\.embField is accessed without holding Mixed\.Mutex`
}

func (m *Mixed) GetNamed() int {
	return m.namedField // want `field Mixed\.namedField is accessed without holding Mixed\.namedMu`
}

// --- Test 7: Struct embedding non-mutex type — no spurious diagnostics ---

type Logger struct {
	prefix string
}

type App struct {
	Logger
	mu   sync.Mutex
	data int
}

func (a *App) SetData(v int) {
	a.mu.Lock()
	a.data = v
	a.mu.Unlock()
}

func (a *App) GetData() int {
	return a.data // want `field App\.data is accessed without holding App\.mu`
}

func (a *App) GetPrefix() string {
	return a.prefix // no diagnostic — prefix has nothing to do with mu
}
