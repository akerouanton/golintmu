package branch_patterns

import "sync"

// --- Pattern 1: Lock held across both if-branches → no diagnostic ---

type BothBranches struct {
	mu    sync.Mutex
	count int
}

func (b *BothBranches) Update(cond bool) {
	b.mu.Lock()
	if cond {
		b.count = 1 // locked on both branches
	} else {
		b.count = 2 // locked on both branches
	}
	b.mu.Unlock()
}

func (b *BothBranches) Get() int {
	return b.count // want `field BothBranches\.count is accessed without holding BothBranches\.mu`
}

// --- Pattern 2: Lock in one if-branch only → inconsistent lock at merge ---

type OneBranch struct {
	mu    sync.Mutex
	value int
}

func (o *OneBranch) Set(v int) {
	o.mu.Lock()
	o.value = v // locked write — guard inference
	o.mu.Unlock()
}

func (o *OneBranch) ConditionalLock(cond bool) {
	if cond {
		o.mu.Lock()
	}
	// After the if merge point, lock is held on one branch but not the other.
	o.value = 42 // want `inconsistent lock state: OneBranch\.mu is held on one branch but not the other`
}

// --- Pattern 3: Conditional lock, unconditional unlock → inconsistent lock at merge ---

type ConditionalPair struct {
	mu    sync.Mutex
	value int
}

func (c *ConditionalPair) Set(v int) {
	c.mu.Lock()
	c.value = v // locked write — guard inference
	c.mu.Unlock()
}

func (c *ConditionalPair) BadPattern(cond bool) {
	if cond {
		c.mu.Lock()
	}
	// merge: lock held on one branch but not the other
	c.value = 10 // want `inconsistent lock state: ConditionalPair\.mu is held on one branch but not the other`
}

// --- Pattern 4: Lock held throughout loop body → no diagnostic ---

type LoopLocked struct {
	mu    sync.Mutex
	count int
}

func (l *LoopLocked) AddN(n int) {
	l.mu.Lock()
	for i := 0; i < n; i++ {
		l.count++ // locked across whole loop — no diagnostic
	}
	l.mu.Unlock()
}

func (l *LoopLocked) Get() int {
	return l.count // want `field LoopLocked\.count is accessed without holding LoopLocked\.mu`
}

// --- Pattern 5: Lock/unlock pair inside loop → no diagnostic ---

type LoopPair struct {
	mu    sync.Mutex
	count int
}

func (l *LoopPair) AddN(n int) {
	for i := 0; i < n; i++ {
		l.mu.Lock()
		l.count++ // locked
		l.mu.Unlock()
	}
}

func (l *LoopPair) Get() int {
	return l.count // want `field LoopPair\.count is accessed without holding LoopPair\.mu`
}

// --- Pattern 6: Lock around if, access inside if → no diagnostic ---

type LockAroundIf struct {
	mu    sync.Mutex
	value int
}

func (l *LockAroundIf) Update(cond bool, v int) {
	l.mu.Lock()
	if cond {
		l.value = v // locked — no diagnostic
	}
	l.mu.Unlock()
}

func (l *LockAroundIf) Get() int {
	return l.value // want `field LockAroundIf\.value is accessed without holding LockAroundIf\.mu`
}

// --- Pattern 7: Switch with lock in some cases only → inconsistent lock at merge ---

type SwitchPartial struct {
	mu    sync.Mutex
	state int
}

func (s *SwitchPartial) Set(v int) {
	s.mu.Lock()
	s.state = v // locked write — guard inference
	s.mu.Unlock()
}

func (s *SwitchPartial) BadSwitch(mode int) {
	switch mode {
	case 1:
		s.mu.Lock()
	case 2:
		s.mu.Lock()
	default:
		// no lock
	}
	// merge: lock held on cases 1,2 but not default
	s.state = mode // want `inconsistent lock state: SwitchPartial\.mu is held on one branch but not the other`
}
