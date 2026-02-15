package false_positives

import "sync"

// --- Test 1: NewConfig constructor → writes excluded; unlocked non-constructor read flagged ---

type Config struct {
	mu      sync.Mutex
	timeout int
}

func NewConfig(timeout int) *Config {
	c := &Config{}
	c.timeout = timeout // constructor — excluded
	return c
}

func (c *Config) SetTimeout(t int) {
	c.mu.Lock()
	c.timeout = t // locked write → guard inferred
	c.mu.Unlock()
}

func (c *Config) GetTimeout() int {
	return c.timeout // want `field Config\.timeout is accessed without holding Config\.mu`
}

// --- Test 2: MakeBuffer constructor → Make* prefix works ---

type Buffer struct {
	mu   sync.Mutex
	data []byte
}

func MakeBuffer(size int) *Buffer {
	b := &Buffer{}
	b.data = make([]byte, size) // constructor — excluded
	return b
}

func (b *Buffer) Write(p []byte) {
	b.mu.Lock()
	b.data = append(b.data, p...) // locked write → guard inferred
	b.mu.Unlock()
}

func (b *Buffer) UnsafeData() []byte {
	return b.data // want `field Buffer\.data is accessed without holding Buffer\.mu`
}

// --- Test 3: CreateSession constructor → Create* prefix works ---

type Session struct {
	mu    sync.Mutex
	token string
}

func CreateSession(token string) *Session {
	s := &Session{}
	s.token = token // constructor — excluded
	return s
}

func (s *Session) Refresh(token string) {
	s.mu.Lock()
	s.token = token // locked write → guard inferred
	s.mu.Unlock()
}

func (s *Session) UnsafeToken() string {
	return s.token // want `field Session\.token is accessed without holding Session\.mu`
}

// --- Test 4: buildPool returns *Pool (no prefix) → return-type constructor detection ---

type Pool struct {
	mu      sync.Mutex
	workers int
}

func buildPool(n int) *Pool {
	p := &Pool{}
	p.workers = n // constructor — excluded via return type
	return p
}

func (p *Pool) AddWorker() {
	p.mu.Lock()
	p.workers++ // locked write → guard inferred
	p.mu.Unlock()
}

func (p *Pool) WorkerCount() int {
	return p.workers // want `field Pool\.workers is accessed without holding Pool\.mu`
}

// --- Test 5: Purely immutable struct → no diagnostics at all ---

type Immutable struct {
	mu   sync.Mutex
	name string
	id   int
}

func NewImmutable(name string, id int) *Immutable {
	return &Immutable{name: name, id: id}
}

func (im *Immutable) Name() string {
	return im.name // no diagnostic: name is immutable (only written in constructor)
}

func (im *Immutable) ID() int {
	return im.id // no diagnostic: id is immutable (only written in constructor)
}

// --- Test 6: init() function writes → excluded from inference ---

type Registry struct {
	mu      sync.Mutex
	entries []string
}

var globalRegistry Registry

func init() {
	globalRegistry.entries = []string{"default"} // init — excluded
}

func (r *Registry) Add(entry string) {
	r.mu.Lock()
	r.entries = append(r.entries, entry) // locked write → guard inferred
	r.mu.Unlock()
}

func (r *Registry) UnsafeEntries() []string {
	return r.entries // want `field Registry\.entries is accessed without holding Registry\.mu`
}

// --- Test 7: Unguarded fields — never locked → no guard inferred, no diagnostics ---

type Unguarded struct {
	mu   sync.Mutex
	name string
	age  int
}

func (u *Unguarded) SetName(n string) {
	u.name = n // no lock ever held
}

func (u *Unguarded) GetName() string {
	return u.name // no diagnostic: no guard inferred
}

func (u *Unguarded) SetAge(a int) {
	u.age = a // no lock ever held
}

func (u *Unguarded) GetAge() int {
	return u.age // no diagnostic: no guard inferred
}

// --- Test 8: Constructor returning by value (not pointer) → returnsStructType handles value returns ---

type Settings struct {
	mu       sync.Mutex
	maxConns int
}

func DefaultSettings() Settings {
	s := Settings{}
	s.maxConns = 10 // constructor — excluded (returns Settings by value)
	return s
}

func (s *Settings) SetMaxConns(n int) {
	s.mu.Lock()
	s.maxConns = n // locked write → guard inferred
	s.mu.Unlock()
}

func (s *Settings) MaxConns() int {
	return s.maxConns // want `field Settings\.maxConns is accessed without holding Settings\.mu`
}
