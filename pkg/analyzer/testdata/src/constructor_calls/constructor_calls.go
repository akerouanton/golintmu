package constructor_calls

import "sync"

// Manager holds a map of configs — the publication target.
type Manager struct {
	configs map[string]*Config
}

// Config is a struct with a guarded field.
type Config struct {
	mu    sync.Mutex
	value string
}

// Read accesses value under mu, establishing the guard.
func (c *Config) Read() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.value
}

// Set writes value under mu.
func (c *Config) Set(v string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.value = v
}

// setup writes value WITHOUT mu — generates a requirement.
func (c *Config) setup() {
	c.value = "initialized"
}

// CreateConfig constructs a Config and calls setup BEFORE publishing to shared
// state. The pre-publication call to setup() should NOT propagate Config.mu
// requirement to CreateConfig.
func (m *Manager) CreateConfig(name string) *Config {
	c := &Config{}
	c.setup() // no diagnostic: pre-publication
	m.configs[name] = c
	return c
}

// CreateAndSetupConfig publishes the Config BEFORE calling setup.
// The post-publication call to setup() propagates Config.mu requirement
// upward, causing a diagnostic at the call site in RunManager.
func (m *Manager) CreateAndSetupConfig(name string) {
	c := &Config{}
	m.configs[name] = c
	c.setup() // post-publication: requirement propagated to caller
}

// UnsafeSetup is not a constructor — retrieves from the map and calls setup.
// Requirement propagated to call site.
func (m *Manager) UnsafeSetup(name string) {
	c := m.configs[name]
	c.setup() // not a constructor: requirement propagated to caller
}

// RunManager exercises all three patterns from concurrent goroutines.
// Diagnostics appear here because requirements propagate to this outermost level.
func RunManager(m *Manager) {
	go func() {
		m.CreateConfig("a") // no diagnostic: setup() was pre-publication
	}()
	go func() {
		m.CreateAndSetupConfig("b") // want `Config\.mu must be held when calling CreateAndSetupConfig\(\)`
	}()
	go func() {
		m.UnsafeSetup("c") // want `Config\.mu must be held when calling UnsafeSetup\(\)`
	}()
}
