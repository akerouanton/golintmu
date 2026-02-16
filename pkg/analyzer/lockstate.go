package analyzer

import (
	"go/token"
	"sort"

	"golang.org/x/tools/go/ssa"
)

// lockRefKind identifies the kind of lock origin.
type lockRefKind int

const (
	fieldLock lockRefKind = iota // mutex is a field of a struct
)

// lockRef identifies a specific lock instance. Two lockRefs are equal when they
// refer to the same logical lock (same base SSA value and field index path).
// The base field is canonicalized via canonicalizeBase to follow through UnOp
// dereferences from SSA variable lifting (closures capturing variables), so
// that multiple loads from the same heap cell resolve to the same lockRef.
type lockRef struct {
	kind       lockRefKind
	base       ssa.Value // canonical SSA value for the struct containing the mutex
	fieldIndex int       // field index within the struct
}

// heldLock is a lockRef with an exclusive flag.
// exclusive is true for Lock(), false for RLock().
type heldLock struct {
	ref       lockRef
	exclusive bool
	pos       token.Pos // position where the lock was acquired
}

// lockState tracks which locks are currently held at a given program point.
type lockState struct {
	held            map[lockRef]heldLock
	deferredUnlocks map[lockRef]bool // locks with pending deferred unlock on this path
}

func newLockState() *lockState {
	return &lockState{
		held:            make(map[lockRef]heldLock),
		deferredUnlocks: make(map[lockRef]bool),
	}
}

// lock adds a lock to the held set.
func (ls *lockState) lock(ref lockRef, exclusive bool, pos token.Pos) {
	ls.held[ref] = heldLock{ref: ref, exclusive: exclusive, pos: pos}
}

// unlock removes a lock from the held set.
func (ls *lockState) unlock(ref lockRef) {
	delete(ls.held, ref)
}

// fork returns a shallow copy of the lock state for use at branch points.
func (ls *lockState) fork() *lockState {
	cp := &lockState{
		held:            make(map[lockRef]heldLock, len(ls.held)),
		deferredUnlocks: make(map[lockRef]bool, len(ls.deferredUnlocks)),
	}
	for k, v := range ls.held {
		cp.held[k] = v
	}
	for k, v := range ls.deferredUnlocks {
		cp.deferredUnlocks[k] = v
	}
	return cp
}

// equalHeld returns true if both states hold exactly the same set of lockRef keys
// and the same set of deferred unlocks.
func (ls *lockState) equalHeld(other *lockState) bool {
	if len(ls.held) != len(other.held) {
		return false
	}
	for k := range ls.held {
		if _, ok := other.held[k]; !ok {
			return false
		}
	}
	if len(ls.deferredUnlocks) != len(other.deferredUnlocks) {
		return false
	}
	for k := range ls.deferredUnlocks {
		if _, ok := other.deferredUnlocks[k]; !ok {
			return false
		}
	}
	return true
}

// diff returns locks held in one state but not the other.
// Results are sorted by fieldIndex for determinism.
func (ls *lockState) diff(other *lockState) (onlyInSelf, onlyInOther []lockRef) {
	for k := range ls.held {
		if _, ok := other.held[k]; !ok {
			onlyInSelf = append(onlyInSelf, k)
		}
	}
	for k := range other.held {
		if _, ok := ls.held[k]; !ok {
			onlyInOther = append(onlyInOther, k)
		}
	}
	sort.Slice(onlyInSelf, func(i, j int) bool {
		return onlyInSelf[i].fieldIndex < onlyInSelf[j].fieldIndex
	})
	sort.Slice(onlyInOther, func(i, j int) bool {
		return onlyInOther[i].fieldIndex < onlyInOther[j].fieldIndex
	})
	return
}

// intersect returns a new state with only locks held in both states.
// Used at merge points for conservative continued analysis.
// When both states hold the same lock but with different modes (exclusive vs shared),
// the lock is dropped from the result to avoid false diagnostics.
func (ls *lockState) intersect(other *lockState) *lockState {
	result := newLockState()
	for k, v := range ls.held {
		if otherHeld, ok := other.held[k]; ok {
			if v.exclusive == otherHeld.exclusive {
				result.held[k] = v
			}
		}
	}
	for k := range ls.deferredUnlocks {
		if other.deferredUnlocks[k] {
			result.deferredUnlocks[k] = true
		}
	}
	return result
}

// deferUnlock marks a lock as having a pending deferred unlock on this path.
func (ls *lockState) deferUnlock(ref lockRef) {
	ls.deferredUnlocks[ref] = true
}
