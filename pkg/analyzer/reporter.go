package analyzer

import (
	"fmt"
	"go/token"
	"go/types"

	"golang.org/x/tools/go/ssa"
)

// checkViolations re-walks observations and reports fields accessed without
// their inferred guard lock held. Functions that have requirements propagated
// upward AND have callers are suppressed here — violations appear at call sites.
func (ctx *passContext) checkViolations() {
	for key, guard := range ctx.guards {
		observations := ctx.observations[key]
		for _, obs := range observations {
			if isConstructorLike(obs.Func, key.StructType) {
				continue
			}

			// Check if the guard mutex is held on the same struct instance.
			held := false
			for _, mutexFieldIdx := range obs.SameBaseMutexFields {
				if mutexFieldIdx == guard.MutexFieldIndex {
					held = true
					break
				}
			}

			if !held {
				// Suppress direct violation if this function has a requirement
				// for this lock and has callers — the violation will be reported
				// at the call sites instead.
				mfk := mutexFieldKey{
					StructType: key.StructType,
					FieldIndex: guard.MutexFieldIndex,
				}
				if ctx.shouldSuppressDirectViolation(obs.Func, mfk) {
					continue
				}
				ctx.reportViolation(obs, key, guard)
			}
		}
	}
}

// shouldSuppressDirectViolation returns true if the function has a requirement
// for the given mutex and has callers (violations reported at call sites instead).
func (ctx *passContext) shouldSuppressDirectViolation(fn *ssa.Function, mfk mutexFieldKey) bool {
	facts, ok := ctx.funcFacts[fn]
	if !ok {
		return false
	}
	if !facts.Requires[mfk] {
		return false
	}
	return ctx.hasCallers(fn)
}

// reportViolation emits a diagnostic for a field access without the required lock.
func (ctx *passContext) reportViolation(obs observation, key fieldKey, guard guardInfo) {
	structName := key.StructType.Obj().Name()
	st, ok := key.StructType.Underlying().(*types.Struct)
	if !ok {
		return
	}
	if key.FieldIndex >= st.NumFields() || guard.MutexFieldIndex >= st.NumFields() {
		return
	}
	fieldName := st.Field(key.FieldIndex).Name()
	mutexName := st.Field(guard.MutexFieldIndex).Name()

	msg := fmt.Sprintf("field %s.%s is accessed without holding %s.%s",
		structName, fieldName, structName, mutexName)

	ctx.pass.Reportf(obs.Pos, "%s", msg)
}

// reportDoubleLock emits a diagnostic for acquiring a lock that is already held.
func (ctx *passContext) reportDoubleLock(pos token.Pos, ref *lockRef) {
	name := lockRefName(*ref)
	if name == "" {
		return
	}
	ctx.pass.Reportf(pos, "%s is already held when locking %s", name, name)
}

// checkInterproceduralViolations iterates call sites and reports:
// - Missing lock at call site (callee requires lock, caller doesn't hold it)
// - Double-lock at call site (caller holds lock, callee acquires it transitively)
func (ctx *passContext) checkInterproceduralViolations() {
	for _, cs := range ctx.callSites {
		calleeFacts, ok := ctx.funcFacts[cs.Callee]
		if !ok {
			continue
		}

		// Check unsatisfied requirements.
		for mfk := range calleeFacts.Requires {
			if !callerHoldsMutex(cs, mfk) {
				// Suppress if the caller also has this requirement propagated
				// and has its own callers — the violation will be reported at
				// the caller's call sites instead.
				if ctx.shouldSuppressDirectViolation(cs.Caller, mfk) {
					continue
				}
				ctx.reportMissingLockAtCallSite(cs, mfk)
			}
		}

		// Check double-locks: caller holds a lock that callee acquires transitively.
		for mfk := range calleeFacts.AcquiresTransitive {
			if callerHoldsMutex(cs, mfk) {
				ctx.reportDoubleLockAtCallSite(cs, mfk)
			}
		}
	}
}

// reportMissingLockAtCallSite emits a diagnostic for a call where the callee
// requires a lock that the caller doesn't hold.
func (ctx *passContext) reportMissingLockAtCallSite(cs callSiteRecord, mfk mutexFieldKey) {
	name := mutexFieldKeyName(mfk)
	if name == "" {
		return
	}
	ctx.pass.Reportf(cs.Pos, "%s must be held when calling %s()", name, cs.Callee.Name())
}

// reportDoubleLockAtCallSite emits a diagnostic for a call where the caller
// holds a lock that the callee also acquires.
func (ctx *passContext) reportDoubleLockAtCallSite(cs callSiteRecord, mfk mutexFieldKey) {
	name := mutexFieldKeyName(mfk)
	if name == "" {
		return
	}
	ctx.pass.Reportf(cs.Pos, "%s is already held when calling %s() which locks %s",
		name, cs.Callee.Name(), name)
}

// mutexFieldKeyName resolves a mutexFieldKey to "StructName.fieldName".
func mutexFieldKeyName(mfk mutexFieldKey) string {
	st, ok := mfk.StructType.Underlying().(*types.Struct)
	if !ok || mfk.FieldIndex >= st.NumFields() {
		return ""
	}
	return mfk.StructType.Obj().Name() + "." + st.Field(mfk.FieldIndex).Name()
}

// reportInconsistentLockState emits a diagnostic for inconsistent lock state
// at a merge point. It diffs the two incoming states and reports each lock held
// on one branch but not the other.
func (ctx *passContext) reportInconsistentLockState(block *ssa.BasicBlock, stateA, stateB *lockState) {
	onlyA, onlyB := stateA.diff(stateB)

	pos := blockPos(block)
	if !pos.IsValid() {
		return
	}

	// Collect unique lock refs from both sides. diff() returns sorted slices,
	// so we merge them in order for deterministic output.
	seen := make(map[lockRef]bool)
	for _, refs := range [2][]lockRef{onlyA, onlyB} {
		for _, ref := range refs {
			if seen[ref] {
				continue
			}
			seen[ref] = true
			name := lockRefName(ref)
			if name == "" {
				continue
			}
			ctx.pass.Reportf(pos, "inconsistent lock state: %s is held on one branch but not the other", name)
		}
	}
}

// lockRefName resolves a lockRef to "StructName.fieldName" for diagnostics.
func lockRefName(ref lockRef) string {
	if ref.kind != fieldLock {
		return ""
	}
	ptrType, ok := ref.base.Type().Underlying().(*types.Pointer)
	if !ok {
		return ""
	}
	named, ok := ptrType.Elem().(*types.Named)
	if !ok {
		return ""
	}
	st, ok := named.Underlying().(*types.Struct)
	if !ok || ref.fieldIndex >= st.NumFields() {
		return ""
	}
	return named.Obj().Name() + "." + st.Field(ref.fieldIndex).Name()
}

// blockPos returns the position of the first non-Phi instruction in a block.
func blockPos(block *ssa.BasicBlock) token.Pos {
	for _, instr := range block.Instrs {
		if _, ok := instr.(*ssa.Phi); ok {
			continue
		}
		if pos := instr.Pos(); pos.IsValid() {
			return pos
		}
	}
	return token.NoPos
}
