package analyzer

import "golang.org/x/tools/go/ssa"

// lockRefKind identifies the kind of lock origin.
type lockRefKind int

const (
	fieldLock lockRefKind = iota // mutex is a field of a struct
)

// lockRef identifies a specific lock instance. Two lockRefs are equal when they
// refer to the same logical lock (same base SSA value and field index path).
type lockRef struct {
	kind       lockRefKind
	base       ssa.Value // the SSA value for the struct containing the mutex
	fieldIndex int       // field index within the struct
}

// heldLock is a lockRef with an exclusive flag. For MVP, exclusive is always true.
type heldLock struct {
	ref       lockRef
	exclusive bool // always true for MVP (no RLock distinction)
}

// lockState tracks which locks are currently held at a given program point.
type lockState struct {
	held map[lockRef]heldLock
}

func newLockState() *lockState {
	return &lockState{held: make(map[lockRef]heldLock)}
}

// lock adds a lock to the held set.
func (ls *lockState) lock(ref lockRef, exclusive bool) {
	ls.held[ref] = heldLock{ref: ref, exclusive: exclusive}
}

// unlock removes a lock from the held set.
func (ls *lockState) unlock(ref lockRef) {
	delete(ls.held, ref)
}

// clone returns a shallow copy of the lock state (simple map copy, no COW).
func (ls *lockState) clone() *lockState {
	cp := &lockState{held: make(map[lockRef]heldLock, len(ls.held))}
	for k, v := range ls.held {
		cp.held[k] = v
	}
	return cp
}
