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
			heldExclusive := false
			for _, hmf := range obs.SameBaseMutexFields {
				if hmf.FieldIndex == guard.MutexFieldIndex {
					held = true
					heldExclusive = hmf.Exclusive
					break
				}
			}

			// Write under RLock: the guard is held but only as shared — data race.
			if held && !obs.IsRead && !heldExclusive {
				if ctx.isConcurrent(obs.Func) {
					ctx.reportWriteUnderSharedLock(obs, key, guard)
				}
				continue
			}

			if !held {
				// Skip violations in non-concurrent contexts.
				if !ctx.isConcurrent(obs.Func) {
					continue
				}
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
	if ctx.isSuppressed(obs.Func, obs.Pos) {
		return
	}
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

// reportWriteUnderSharedLock emits a diagnostic for writing a field while only
// holding a read lock (RLock) — this is a data race since RLock doesn't provide
// mutual exclusion for writes.
func (ctx *passContext) reportWriteUnderSharedLock(obs observation, key fieldKey, guard guardInfo) {
	if ctx.isSuppressed(obs.Func, obs.Pos) {
		return
	}
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

	msg := fmt.Sprintf("field %s.%s is written while %s.%s is read-locked \u2014 use Lock() for write access",
		structName, fieldName, structName, mutexName)

	ctx.pass.Reportf(obs.Pos, "%s", msg)
}

// reportDoubleLock emits a diagnostic for acquiring a lock that is already held.
func (ctx *passContext) reportDoubleLock(fn *ssa.Function, pos token.Pos, ref *lockRef) {
	if ctx.isSuppressed(fn, pos) {
		return
	}
	name := lockRefName(*ref)
	if name == "" {
		return
	}
	ctx.pass.Reportf(pos, "%s is already held when locking %s", name, name)
}

// reportRecursiveRLock emits a diagnostic for recursive RLock — can deadlock
// if a writer is waiting.
func (ctx *passContext) reportRecursiveRLock(fn *ssa.Function, pos token.Pos, ref *lockRef) {
	if ctx.isSuppressed(fn, pos) {
		return
	}
	name := lockRefName(*ref)
	if name == "" {
		return
	}
	ctx.pass.Reportf(pos, "recursive RLock on %s \u2014 can deadlock if a writer is waiting", name)
}

// reportLockUpgradeAttempt emits a diagnostic for Lock() while RLock is held — deadlock.
func (ctx *passContext) reportLockUpgradeAttempt(fn *ssa.Function, pos token.Pos, ref *lockRef) {
	if ctx.isSuppressed(fn, pos) {
		return
	}
	name := lockRefName(*ref)
	if name == "" {
		return
	}
	ctx.pass.Reportf(pos, "%s.Lock() called while %s is read-locked \u2014 lock upgrade can deadlock", name, name)
}

// reportMismatchedUnlock emits a diagnostic for calling the wrong unlock method.
func (ctx *passContext) reportMismatchedUnlock(fn *ssa.Function, pos token.Pos, ref *lockRef, wasExclusive bool, unlockMethod string) {
	if ctx.isSuppressed(fn, pos) {
		return
	}
	name := lockRefName(*ref)
	if name == "" {
		return
	}
	if wasExclusive {
		ctx.pass.Reportf(pos, "%s is exclusively locked but %s() was called \u2014 use Unlock()", name, unlockMethod)
	} else {
		ctx.pass.Reportf(pos, "%s is read-locked but %s() was called \u2014 use RUnlock()", name, unlockMethod)
	}
}

// checkExportedGuardedFields warns about exported fields that are guarded by
// a lock. External packages can bypass the lock by accessing the field directly.
func (ctx *passContext) checkExportedGuardedFields() {
	for key, guard := range ctx.guards {
		// Only check types defined in this package.
		if key.StructType.Obj().Pkg() != ctx.pass.Pkg {
			continue
		}

		st, ok := key.StructType.Underlying().(*types.Struct)
		if !ok {
			continue
		}
		if key.FieldIndex >= st.NumFields() {
			continue
		}

		field := st.Field(key.FieldIndex)
		if !field.Exported() {
			continue
		}

		ctx.reportExportedGuardedField(key, guard, field)
	}
}

// reportExportedGuardedField emits a C14 diagnostic for an exported guarded field.
func (ctx *passContext) reportExportedGuardedField(key fieldKey, guard guardInfo, field *types.Var) {
	st, ok := key.StructType.Underlying().(*types.Struct)
	if !ok || guard.MutexFieldIndex >= st.NumFields() {
		return
	}

	structName := key.StructType.Obj().Name()
	fieldName := field.Name()
	mutexName := st.Field(guard.MutexFieldIndex).Name()

	msg := fmt.Sprintf("field %s.%s is guarded by %s.%s but is exported \u2014 external packages can bypass the lock",
		structName, fieldName, structName, mutexName)

	ctx.pass.Reportf(field.Pos(), "%s", msg)
}

// checkInterproceduralViolations iterates call sites and reports:
// - Missing lock at call site (callee requires lock, caller doesn't hold it)
// - Double-lock at call site (caller holds lock, callee acquires it transitively)
func (ctx *passContext) checkInterproceduralViolations() {
	for _, cs := range ctx.callSites {
		// Skip violations in non-concurrent contexts.
		if !ctx.isConcurrent(cs.Caller) {
			continue
		}

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
	if ctx.isSuppressed(cs.Caller, cs.Pos) {
		return
	}
	name := mutexFieldKeyName(mfk)
	if name == "" {
		return
	}
	ctx.pass.Reportf(cs.Pos, "%s must be held when calling %s()", name, cs.Callee.Name())
}

// reportDoubleLockAtCallSite emits a diagnostic for a call where the caller
// holds a lock that the callee also acquires.
func (ctx *passContext) reportDoubleLockAtCallSite(cs callSiteRecord, mfk mutexFieldKey) {
	if ctx.isSuppressed(cs.Caller, cs.Pos) {
		return
	}
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
func (ctx *passContext) reportInconsistentLockState(fn *ssa.Function, block *ssa.BasicBlock, stateA, stateB *lockState) {
	onlyA, onlyB := stateA.diff(stateB)

	pos := blockPos(block)
	if !pos.IsValid() {
		return
	}

	if ctx.isSuppressed(fn, pos) {
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
