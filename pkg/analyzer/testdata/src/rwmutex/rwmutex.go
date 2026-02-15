package rwmutex

import "sync"

// --- Test 1: Mismatched unlock — Unlock() after RLock() ---

type Config struct {
	rw     sync.RWMutex
	values map[string]string
}

func (c *Config) Get(key string) string {
	c.rw.RLock()
	defer c.rw.Unlock() // want `Config\.rw is read-locked but Unlock\(\) was called — use RUnlock\(\)`
	return c.values[key]
}

func (c *Config) Set(key, val string) {
	c.rw.Lock()
	c.values[key] = val
	c.rw.Unlock()
}

// --- Test 2: Mismatched unlock — RUnlock() after Lock() ---

type State struct {
	rw   sync.RWMutex
	data []byte
}

func (s *State) Update(data []byte) {
	s.rw.Lock()
	s.data = data
	s.rw.RUnlock() // want `State\.rw is exclusively locked but RUnlock\(\) was called — use Unlock\(\)`
}

func (s *State) Read() []byte {
	s.rw.RLock()
	d := s.data
	s.rw.RUnlock()
	return d
}

// --- Test 3: Deferred mismatched unlock ---

type Cache struct {
	rw    sync.RWMutex
	items map[string]string
}

func (c *Cache) Lookup(key string) string {
	c.rw.RLock()
	defer c.rw.Unlock() // want `Cache\.rw is read-locked but Unlock\(\) was called — use RUnlock\(\)`
	return c.items[key]
}

func (c *Cache) Store(key, val string) {
	c.rw.Lock()
	c.items[key] = val
	c.rw.Unlock()
}

// --- Test 4: Recursive RLock ---

type Tree struct {
	rw       sync.RWMutex
	children []*Tree
	value    int
}

func (t *Tree) Sum() int {
	t.rw.RLock()
	defer t.rw.RUnlock()

	total := t.value
	_ = total

	t.rw.RLock() // want `recursive RLock on Tree\.rw — can deadlock if a writer is waiting`
	t.rw.RUnlock()
	return total
}

// --- Test 5: Lock upgrade — Lock() after RLock() ---

type Registry struct {
	rw    sync.RWMutex
	items map[string]int
}

func (r *Registry) GetOrCreate(key string) int {
	r.rw.RLock()
	val := r.items[key]
	_ = val
	r.rw.Lock() // want `Registry\.rw\.Lock\(\) called while Registry\.rw is read-locked — lock upgrade can deadlock`
	r.items[key] = 42
	r.rw.Unlock()
	return 0
}

func (r *Registry) Set(key string, val int) {
	r.rw.Lock()
	r.items[key] = val
	r.rw.Unlock()
}

// --- Test 6: Correct RLock/RUnlock — no diagnostic ---

type SafeReader struct {
	rw   sync.RWMutex
	data string
}

func (sr *SafeReader) Read() string {
	sr.rw.RLock()
	defer sr.rw.RUnlock()
	return sr.data
}

func (sr *SafeReader) Write(d string) {
	sr.rw.Lock()
	sr.data = d
	sr.rw.Unlock()
}

// --- Test 7: Correct Lock/Unlock — no diagnostic ---

type SafeWriter struct {
	mu   sync.Mutex
	data int
}

func (sw *SafeWriter) Inc() {
	sw.mu.Lock()
	sw.data++
	sw.mu.Unlock()
}

func (sw *SafeWriter) Get() int {
	sw.mu.Lock()
	defer sw.mu.Unlock()
	return sw.data
}

// --- Test 8: Read under RLock + write under Lock — guard inference works ---

type Mixed struct {
	rw    sync.RWMutex
	count int
}

func (m *Mixed) Read() int {
	m.rw.RLock()
	defer m.rw.RUnlock()
	return m.count // read under RLock
}

func (m *Mixed) Write(v int) {
	m.rw.Lock()
	m.count = v // write under Lock
	m.rw.Unlock()
}

// --- Test 9: Write under RLock — data race ---

type WriteUnderRLock struct {
	rw    sync.RWMutex
	value int
}

func (w *WriteUnderRLock) BadWrite(v int) {
	w.rw.RLock()
	w.value = v // want `field WriteUnderRLock\.value is written while WriteUnderRLock\.rw is read-locked — use Lock\(\) for write access`
	w.rw.RUnlock()
}

func (w *WriteUnderRLock) GoodWrite(v int) {
	w.rw.Lock()
	w.value = v
	w.rw.Unlock()
}

func (w *WriteUnderRLock) GoodRead() int {
	w.rw.RLock()
	defer w.rw.RUnlock()
	return w.value
}

// --- Test 10: Unprotected field access with RWMutex — C1 still detected ---

type Unprotected struct {
	rw    sync.RWMutex
	value int
}

func (u *Unprotected) SafeWrite(v int) {
	u.rw.Lock()
	u.value = v
	u.rw.Unlock()
}

func (u *Unprotected) UnsafeRead() int {
	return u.value // want `field Unprotected\.value is accessed without holding Unprotected\.rw`
}
