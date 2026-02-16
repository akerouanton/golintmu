package unlock_of_unlocked

import "sync"

// --- Unpaired unlock: Unlock() with no preceding Lock() ---

type Unpaired struct {
	mu    sync.Mutex
	value int
}

func (u *Unpaired) Set(v int) {
	u.mu.Lock()
	u.value = v
	u.mu.Unlock()
}

func (u *Unpaired) BadUnlock() {
	u.mu.Unlock() // want `Unlock\(\) called but Unpaired\.mu is not held`
}

// --- Double unlock: second Unlock() after first removed from held ---

type DoubleUnlock struct {
	mu    sync.Mutex
	count int
}

func (d *DoubleUnlock) Set(v int) {
	d.mu.Lock()
	d.count = v
	d.mu.Unlock()
}

func (d *DoubleUnlock) BadDoubleUnlock() {
	d.mu.Lock()
	d.count = 1
	d.mu.Unlock()
	d.mu.Unlock() // want `Unlock\(\) called but DoubleUnlock\.mu is not held`
}

// --- Branch-conditional unlock: lock in one branch, unconditional unlock ---

type BranchConditional struct {
	mu    sync.Mutex
	state int
}

func (b *BranchConditional) Set(v int) {
	b.mu.Lock()
	b.state = v
	b.mu.Unlock()
}

func (b *BranchConditional) ConditionalLockUnconditionalUnlock(cond bool) {
	if cond {
		b.mu.Lock()
	}
	b.state = 1    // want `inconsistent lock state: BranchConditional\.mu is held on one branch but not the other`
	b.mu.Unlock()  // want `Unlock\(\) called but BranchConditional\.mu is not held`
}

// --- Deferred pair (suppressed): Lock() + defer Unlock() --- no diagnostic ---

type DeferPair struct {
	mu    sync.Mutex
	value int
}

func (d *DeferPair) Set(v int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.value = v
}

func (d *DeferPair) Get() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.value
}

// --- Helper with Requires (suppressed): function only unlocks, callers hold lock ---

type WithHelper struct {
	mu   sync.Mutex
	data int
}

func (w *WithHelper) Set(v int) {
	w.mu.Lock()
	w.data = v
	w.mu.Unlock()
}

func (w *WithHelper) unsafeRead() int {
	return w.data // accessed without lock — triggers requirement inference
}

func (w *WithHelper) ReadUnderLock() int {
	w.mu.Lock()
	v := w.unsafeRead()
	w.mu.Unlock()
	return v
}
