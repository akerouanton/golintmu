package analyzer

import (
	"fmt"
	"go/token"
	"go/types"
	"path/filepath"

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

// computeReturnsHolding derives per-function ReturnsHolding postconditions from
// C5 lock-leak candidates. A function has ReturnsHolding(mfk) if ALL its return
// points hold lock mfk (every return is a lock-leak candidate for that lock).
func (ctx *passContext) computeReturnsHolding() {
	// Group lock-leak candidates by (function, mutexFieldKey) → set of return positions.
	type fnMfk struct {
		fn  *ssa.Function
		mfk mutexFieldKey
	}
	returnPositions := make(map[fnMfk]map[token.Pos]bool)

	for _, candidates := range ctx.lockLeakCandidates {
		for _, c := range candidates {
			mfk, ok := lockRefToMutexFieldKey(&c.Ref)
			if !ok {
				continue
			}
			key := fnMfk{fn: c.Fn, mfk: mfk}
			if returnPositions[key] == nil {
				returnPositions[key] = make(map[token.Pos]bool)
			}
			returnPositions[key][c.Pos] = true
		}
	}

	// Count actual *ssa.Return instructions per function.
	returnCount := make(map[*ssa.Function]int)
	for _, fn := range ctx.srcFuncs {
		for _, block := range fn.Blocks {
			for _, instr := range block.Instrs {
				if _, ok := instr.(*ssa.Return); ok {
					returnCount[fn]++
				}
			}
		}
	}

	// A function has ReturnsHolding(mfk) if every return is a candidate.
	for key, positions := range returnPositions {
		if len(positions) == returnCount[key.fn] && returnCount[key.fn] > 0 {
			ctx.getOrCreateFuncFacts(key.fn).ReturnsHolding[key.mfk] = true
		}
	}
}

// checkCallersOfAcquireHelpers checks callers of acquire helpers (functions with
// ReturnsHolding) and reports when a caller never releases the acquired lock.
func (ctx *passContext) checkCallersOfAcquireHelpers() {
	// First, report callee-side diagnostics for acquire helpers.
	for fn, facts := range ctx.funcFacts {
		for mfk := range facts.ReturnsHolding {
			ctx.reportAcquireHelper(fn, mfk)
		}
	}

	// Then, check callers.
	for _, cs := range ctx.callSites {
		calleeFacts, ok := ctx.funcFacts[cs.Callee]
		if !ok {
			continue
		}
		if len(calleeFacts.ReturnsHolding) == 0 {
			continue
		}

		callerFacts := ctx.getOrCreateFuncFacts(cs.Caller)

		for mfk := range calleeFacts.ReturnsHolding {
			// Caller propagates the postcondition (itself an acquire helper).
			if callerFacts.ReturnsHolding[mfk] {
				continue
			}
			// Caller releases the lock.
			if callerFacts.Releases[mfk] {
				continue
			}
			ctx.reportCallerMissingUnlock(cs, mfk)
		}
	}
}

// reportAcquireHelper emits a callee-side C13 diagnostic for a function that
// returns while holding a lock on all paths.
func (ctx *passContext) reportAcquireHelper(fn *ssa.Function, mfk mutexFieldKey) {
	if ctx.isSuppressed(fn, fn.Pos()) {
		return
	}
	name := mutexFieldKeyName(mfk)
	if name == "" {
		return
	}
	ctx.pass.Reportf(fn.Pos(), "%s() returns while holding %s -- callers must unlock", fn.Name(), name)
}

// reportCallerMissingUnlock emits a caller-side C13 diagnostic for a caller
// that never releases the lock acquired by an acquire helper.
func (ctx *passContext) reportCallerMissingUnlock(cs callSiteRecord, mfk mutexFieldKey) {
	if ctx.isSuppressed(cs.Caller, cs.Pos) {
		return
	}
	name := mutexFieldKeyName(mfk)
	if name == "" {
		return
	}
	ctx.pass.Reportf(cs.Pos, "%s() calls %s() which acquires %s, but %s() never releases it",
		cs.Caller.Name(), cs.Callee.Name(), name, cs.Caller.Name())
}

// reportDeferredLockLeaks iterates C5 candidates collected during Phase 1
// and reports those not suppressed by funcLockFacts.Requires.
func (ctx *passContext) reportDeferredLockLeaks() {
	for _, candidates := range ctx.lockLeakCandidates {
		for _, c := range candidates {
			if ctx.functionRequiresMutex(c.Fn, &c.Ref) {
				continue
			}
			// Suppress C5 when C13 applies: the function is an acquire helper
			// for this lock, so the leak is an intentional postcondition.
			mfk, ok := lockRefToMutexFieldKey(&c.Ref)
			if ok && ctx.funcFacts[c.Fn] != nil && ctx.funcFacts[c.Fn].ReturnsHolding[mfk] {
				continue
			}
			ctx.reportLockLeak(c)
		}
	}
}

// reportLockLeak emits a C5 diagnostic for returning without unlocking a held mutex.
func (ctx *passContext) reportLockLeak(c lockLeakCandidate) {
	if ctx.isSuppressed(c.Fn, c.Pos) {
		return
	}
	name := lockRefName(c.Ref)
	if name == "" {
		return
	}
	acquirePos := ctx.pass.Fset.Position(c.AcquirePos)
	ctx.pass.Reportf(c.Pos, "return without unlocking %s (locked at %s:%d:%d)",
		name, filepath.Base(acquirePos.Filename), acquirePos.Line, acquirePos.Column)
}

// reportDeferredUnlockOfUnlocked iterates C4 candidates collected during Phase 1
// and reports those not suppressed by funcLockFacts.Requires or acquire helpers.
func (ctx *passContext) reportDeferredUnlockOfUnlocked() {
	for _, c := range ctx.unlockOfUnlockedCandidates {
		if ctx.functionRequiresMutex(c.Fn, &c.Ref) {
			continue
		}
		// Suppress C4 when unlocking a lock returned-holding by a callee.
		// The unlock is expected: the callee acquired the lock and the
		// caller is correctly releasing it.
		mfk, ok := lockRefToMutexFieldKey(&c.Ref)
		if ok && ctx.calleeReturnsHolding(c.Fn, mfk) {
			continue
		}
		ctx.reportUnlockOfUnlocked(c.Fn, c.Pos, &c.Ref)
	}
}

// calleeReturnsHolding returns true if any callee of fn has ReturnsHolding for mfk.
func (ctx *passContext) calleeReturnsHolding(fn *ssa.Function, mfk mutexFieldKey) bool {
	for _, cs := range ctx.callSites {
		if cs.Caller != fn {
			continue
		}
		calleeFacts, ok := ctx.funcFacts[cs.Callee]
		if !ok {
			continue
		}
		if calleeFacts.ReturnsHolding[mfk] {
			return true
		}
	}
	return false
}

// functionRequiresMutex returns true if fn's funcLockFacts.Requires contains
// the mutex identified by ref. Used to suppress C4 for helper functions that
// expect callers to hold the lock.
func (ctx *passContext) functionRequiresMutex(fn *ssa.Function, ref *lockRef) bool {
	mfk, ok := lockRefToMutexFieldKey(ref)
	if !ok {
		return false
	}
	facts, ok := ctx.funcFacts[fn]
	if !ok {
		return false
	}
	return facts.Requires[mfk]
}

// reportUnlockOfUnlocked emits a C4 diagnostic for Unlock() when the mutex is not held.
func (ctx *passContext) reportUnlockOfUnlocked(fn *ssa.Function, pos token.Pos, ref *lockRef) {
	if ctx.isSuppressed(fn, pos) {
		return
	}
	name := lockRefName(*ref)
	if name == "" {
		return
	}
	ctx.pass.Reportf(pos, "Unlock() called but %s is not held", name)
}

// detectAndReportLockOrderCycles runs cycle detection on the lock-order graph
// and reports violations filtered by concurrent context.
func (ctx *passContext) detectAndReportLockOrderCycles() {
	cycles := ctx.lockOrderGraph.detectCycles()
	for _, cycle := range cycles {
		// Filter: at least one edge must originate from a concurrent function.
		hasConcurrent := false
		for _, edge := range cycle {
			if ctx.isConcurrent(edge.Fn) {
				hasConcurrent = true
				break
			}
		}
		if !hasConcurrent {
			continue
		}
		ctx.reportLockOrderCycle(cycle)
	}
}

// reportLockOrderCycle emits a C3 diagnostic for a lock-ordering cycle.
func (ctx *passContext) reportLockOrderCycle(cycle lockOrderCycle) {
	if len(cycle) == 0 {
		return
	}

	// Use the first edge's position for the diagnostic.
	edge := cycle[0]
	if ctx.isSuppressed(edge.Fn, edge.Pos) {
		return
	}

	// Collect unique mutex names in the cycle.
	seen := make(map[string]bool)
	var names []string
	for _, e := range cycle {
		fromName := mutexFieldKeyName(e.From)
		if fromName != "" && !seen[fromName] {
			seen[fromName] = true
			names = append(names, fromName)
		}
	}

	if len(names) < 2 {
		// Self-edge: same type, different instances.
		if len(names) == 1 {
			ctx.pass.Reportf(edge.Pos, "potential deadlock: lock ordering cycle on %s", names[0])
			return
		}
		return
	}

	ctx.pass.Reportf(edge.Pos, "potential deadlock: lock ordering cycle between %s and %s",
		names[0], names[1])
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
