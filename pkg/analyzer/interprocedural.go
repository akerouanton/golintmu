package analyzer

import (
	"go/token"
	"go/types"

	"golang.org/x/tools/go/ssa"
)

// mutexFieldKey uniquely identifies a mutex field across the package.
type mutexFieldKey struct {
	StructType *types.Named
	FieldIndex int
}

// callSiteRecord records a static call from one function to another,
// along with the normalized lock state at the call site.
type callSiteRecord struct {
	Caller           *ssa.Function
	Callee           *ssa.Function
	Pos              token.Pos
	HeldByStructType map[*types.Named][]int // normalized lock state: struct type → held mutex field indices
}

// funcLockFacts tracks lock requirements and acquisitions for a function.
type funcLockFacts struct {
	Requires           map[mutexFieldKey]bool // locks callers must hold
	Acquires           map[mutexFieldKey]bool // locks this function directly acquires
	AcquiresTransitive map[mutexFieldKey]bool // direct + transitive acquisitions (via callees)
}

// getOrCreateFuncFacts returns the funcLockFacts for a function, creating it if needed.
func (ctx *passContext) getOrCreateFuncFacts(fn *ssa.Function) *funcLockFacts {
	if facts, ok := ctx.funcFacts[fn]; ok {
		return facts
	}
	facts := &funcLockFacts{
		Requires:           make(map[mutexFieldKey]bool),
		Acquires:           make(map[mutexFieldKey]bool),
		AcquiresTransitive: make(map[mutexFieldKey]bool),
	}
	ctx.funcFacts[fn] = facts
	return facts
}

// normalizeLockState converts a function-local lockState to a type-scoped
// representation: map from struct type to held mutex field indices.
func normalizeLockState(ls *lockState) map[*types.Named][]int {
	result := make(map[*types.Named][]int)
	for _, hl := range ls.held {
		if hl.ref.kind != fieldLock {
			continue
		}
		// Resolve the struct type from the base SSA value.
		ptrType, ok := hl.ref.base.Type().Underlying().(*types.Pointer)
		if !ok {
			continue
		}
		named, ok := ptrType.Elem().(*types.Named)
		if !ok {
			continue
		}
		result[named] = append(result[named], hl.ref.fieldIndex)
	}
	return result
}

// deriveInitialRequirements scans observations against inferred guards to
// determine which locks each function needs its callers to hold.
// If function F accesses a guarded field without the guard lock held,
// F requires that lock (unless F is constructor-like).
func (ctx *passContext) deriveInitialRequirements() {
	for key, guard := range ctx.guards {
		for _, obs := range ctx.observations[key] {
			if isConstructorLike(obs.Func, key.StructType) {
				continue
			}

			held := false
			for _, mutexFieldIdx := range obs.SameBaseMutexFields {
				if mutexFieldIdx == guard.MutexFieldIndex {
					held = true
					break
				}
			}

			if !held {
				mfk := mutexFieldKey{
					StructType: key.StructType,
					FieldIndex: guard.MutexFieldIndex,
				}
				facts := ctx.getOrCreateFuncFacts(obs.Func)
				facts.Requires[mfk] = true
			}
		}
	}
}

// propagateRequirements propagates lock requirements bottom-up through the
// intra-package call graph until a fixed point, and propagates acquisitions
// transitively downward for double-lock detection.
func (ctx *passContext) propagateRequirements() {
	// Build reverse call graph: callee → list of call site indices.
	calleeToSites := make(map[*ssa.Function][]int)
	for i, cs := range ctx.callSites {
		calleeToSites[cs.Callee] = append(calleeToSites[cs.Callee], i)
	}

	// Fixed-point loop for requirement propagation (bottom-up).
	// If callee requires lock L and caller doesn't hold L at the call site,
	// then caller also requires L.
	// Termination: requirements are monotonically increasing, bounded by
	// |functions| × |mutex fields|. Safety limit prevents runaway in edge cases.
	const maxIterations = 1000
	changed := true
	for i := 0; changed && i < maxIterations; i++ {
		changed = false
		for callee, facts := range ctx.funcFacts {
			for mfk := range facts.Requires {
				// For each call site that calls this function...
				for _, siteIdx := range calleeToSites[callee] {
					cs := ctx.callSites[siteIdx]
					if callerHoldsMutex(cs, mfk) {
						continue // caller satisfies this requirement
					}
					// Propagate requirement to caller.
					callerFacts := ctx.getOrCreateFuncFacts(cs.Caller)
					if !callerFacts.Requires[mfk] {
						callerFacts.Requires[mfk] = true
						changed = true
					}
				}
			}
		}
	}

	// Propagate AcquiresTransitive: start with direct acquisitions,
	// then add transitive acquisitions from callees.
	for _, facts := range ctx.funcFacts {
		for mfk := range facts.Acquires {
			facts.AcquiresTransitive[mfk] = true
		}
	}

	changed = true
	for i := 0; changed && i < maxIterations; i++ {
		changed = false
		for _, cs := range ctx.callSites {
			calleeFacts, ok := ctx.funcFacts[cs.Callee]
			if !ok {
				continue
			}
			callerFacts := ctx.getOrCreateFuncFacts(cs.Caller)
			for mfk := range calleeFacts.AcquiresTransitive {
				if !callerFacts.AcquiresTransitive[mfk] {
					callerFacts.AcquiresTransitive[mfk] = true
					changed = true
				}
			}
		}
	}
}

// callerHoldsMutex returns true if the call site record indicates the caller
// holds the specified mutex at the call point.
func callerHoldsMutex(cs callSiteRecord, mfk mutexFieldKey) bool {
	heldFields, ok := cs.HeldByStructType[mfk.StructType]
	if !ok {
		return false
	}
	for _, fi := range heldFields {
		if fi == mfk.FieldIndex {
			return true
		}
	}
	return false
}

// hasCallers returns true if the function has any recorded call sites.
func (ctx *passContext) hasCallers(fn *ssa.Function) bool {
	for _, cs := range ctx.callSites {
		if cs.Callee == fn {
			return true
		}
	}
	return false
}
