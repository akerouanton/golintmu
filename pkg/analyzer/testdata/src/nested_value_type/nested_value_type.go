package nested_value_type

import "sync"

// --- Test 1: Basic read of value-type nested field without lock ---

type State struct {
	Status int
}

type Container struct {
	mu    sync.Mutex
	state State
}

func (c *Container) SetStatus(s int) {
	c.mu.Lock()
	c.state.Status = s // locked write → establishes guard on Container.state
	c.mu.Unlock()
}

func (c *Container) GetStatus() int {
	return c.state.Status // want `field Container\.state is accessed without holding Container\.mu`
}

// --- Test 2: Write to value-type nested field without lock ---

func (c *Container) WriteStatusUnlocked(s int) {
	c.state.Status = s // want `field Container\.state is accessed without holding Container\.mu`
}

// --- Test 3: Interprocedural — helper reads nested field, caller doesn't hold lock ---

func (c *Container) isTombstoned() bool {
	return c.state.Status == 1 // (suppressed: requirement propagated to call sites)
}

func (c *Container) SafeCheck() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.isTombstoned()
}

func (c *Container) UnsafeCheck() bool {
	return c.isTombstoned() // want `Container\.mu must be held when calling isTombstoned\(\)`
}

// --- Test 4: Deep nesting (3 levels) ---

type Deep struct {
	Value int
}

type Inner struct {
	deep Deep
}

type Outer struct {
	mu    sync.Mutex
	inner Inner
}

func (o *Outer) SetValue(v int) {
	o.mu.Lock()
	o.inner.deep.Value = v // locked write → establishes guard on Outer.inner
	o.mu.Unlock()
}

func (o *Outer) GetValue() int {
	return o.inner.deep.Value // want `field Outer\.inner is accessed without holding Outer\.mu`
}

// --- Test 5: Constructor exclusion — nested write in NewContainer is excluded ---

func NewContainer() *Container {
	c := &Container{}
	c.state.Status = 0 // constructor: no diagnostic
	return c
}

// --- Test 6: All locked — nested access under lock → no diagnostic ---

func (c *Container) GetStatusSafe() int {
	c.mu.Lock()
	s := c.state.Status // locked: no diagnostic
	c.mu.Unlock()
	return s
}
