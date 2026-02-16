package defer_patterns

import "sync"

// --- Test 1: Lock + defer Unlock, then field access → no diagnostic; unlocked getter flagged ---

type Counter struct {
	mu    sync.Mutex
	count int
}

func (c *Counter) Inc() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.count++ // locked via defer — no diagnostic
}

func (c *Counter) Get() int {
	return c.count // want `field Counter\.count is accessed without holding Counter\.mu`
}

// --- Test 2: Multiple fields accessed under defer unlock → all locked; unlocked getter flagged ---

type MultiField struct {
	mu   sync.Mutex
	name string
	age  int
}

func (m *MultiField) Update(name string, age int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.name = name // locked
	m.age = age   // locked
}

func (m *MultiField) GetName() string {
	return m.name // want `field MultiField\.name is accessed without holding MultiField\.mu`
}

func (m *MultiField) GetAge() int {
	return m.age // want `field MultiField\.age is accessed without holding MultiField\.mu`
}

// --- Test 3: Defer unlock with early return → lock held on all paths; unlocked getter flagged ---

type EarlyReturn struct {
	mu    sync.Mutex
	value int
}

func (e *EarlyReturn) SetIfPositive(v int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if v <= 0 {
		return // early return — defer still unlocks, but lock was held for body
	}
	e.value = v // locked
}

func (e *EarlyReturn) Get() int {
	return e.value // want `field EarlyReturn\.value is accessed without holding EarlyReturn\.mu`
}

// --- Test 4: Explicit unlock (baseline comparison) → same behavior as Counter in basic.go ---

type ExplicitUnlock struct {
	mu    sync.Mutex
	count int
}

func (e *ExplicitUnlock) Inc() {
	e.mu.Lock()
	e.count++ // locked
	e.mu.Unlock()
}

func (e *ExplicitUnlock) Get() int {
	return e.count // want `field ExplicitUnlock\.count is accessed without holding ExplicitUnlock\.mu`
}

// --- Test 5: Read access under defer-locked region → no diagnostic; unlocked read flagged ---

type ReadUnderLock struct {
	mu    sync.Mutex
	items []string
}

func (r *ReadUnderLock) Add(item string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.items = append(r.items, item) // locked write
}

func (r *ReadUnderLock) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.items) // locked read — no diagnostic
}

func (r *ReadUnderLock) UnsafeLen() int {
	return len(r.items) // want `field ReadUnderLock\.items is accessed without holding ReadUnderLock\.mu`
}

// --- Test 6: ALL methods use defer-based unlock → guard inferred; unlocked read flagged ---

type AllDefer struct {
	mu   sync.Mutex
	data int
}

func (a *AllDefer) Set(v int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.data = v // locked
}

func (a *AllDefer) Inc() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.data++ // locked
}

func (a *AllDefer) UnsafeGet() int {
	return a.data // want `field AllDefer\.data is accessed without holding AllDefer\.mu`
}

// --- Test 7: defer mu.Lock() anti-pattern → lock not acquired during body; access flagged ---

type DeferLock struct {
	mu    sync.Mutex
	value int
}

func (d *DeferLock) CorrectSet(v int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.value = v // locked write → guard inferred
}

func (d *DeferLock) BrokenSet(v int) {
	defer d.mu.Lock() // want `defer DeferLock\.mu\.Lock\(\) will deadlock — did you mean defer DeferLock\.mu\.Unlock\(\)\?`
	d.value = v       // want `field DeferLock\.value is accessed without holding DeferLock\.mu`
}
