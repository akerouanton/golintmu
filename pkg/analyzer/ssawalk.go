package analyzer

import (
	"go/token"
	"go/types"

	"golang.org/x/tools/go/ssa"
)

// walkContext holds per-function CFG walk state.
type walkContext struct {
	fn          *ssa.Function
	entryStates map[*ssa.BasicBlock]*lockState // state at block entry
	exitStates  map[*ssa.BasicBlock]*lockState // state at block exit
	inconsistentLockReported map[*ssa.BasicBlock]bool       // blocks where inconsistent lock state was already reported
}

// collectObservations iterates over all source functions and walks their CFGs.
func (ctx *passContext) collectObservations() {
	for _, fn := range ctx.srcFuncs {
		ctx.walkFunction(fn)
	}
}

// walkFunction walks a function's basic blocks tracking lock state.
func (ctx *passContext) walkFunction(fn *ssa.Function) {
	if len(fn.Blocks) == 0 {
		return
	}
	wctx := &walkContext{
		fn:          fn,
		entryStates: make(map[*ssa.BasicBlock]*lockState),
		exitStates:  make(map[*ssa.BasicBlock]*lockState),
		inconsistentLockReported: make(map[*ssa.BasicBlock]bool),
	}
	ls := newLockState()
	ctx.walkBlock(wctx, fn.Blocks[0], nil, ls)
}

// walkBlock processes all instructions in a basic block and recurses into successors.
// It uses entry-state comparison to handle loops and merge points correctly.
func (ctx *passContext) walkBlock(wctx *walkContext, block *ssa.BasicBlock, fromBlock *ssa.BasicBlock, ls *lockState) {
	if prevEntry, visited := wctx.entryStates[block]; visited {
		if ls.equalHeld(prevEntry) {
			return // loop with compatible state, no new info
		}
		// Different state on re-visit → compute intersection
		merged := prevEntry.intersect(ls)
		// Report inconsistent lock state once per merge point (not on loop back-edges)
		if !wctx.inconsistentLockReported[block] && fromBlock != nil && len(block.Preds) > 1 && !isBackEdge(fromBlock, block) {
			ctx.reportInconsistentLockState(wctx.fn, block, prevEntry, ls)
			wctx.inconsistentLockReported[block] = true
		}
		if merged.equalHeld(prevEntry) {
			return // converged
		}
		wctx.entryStates[block] = merged
		ls = merged
	} else {
		wctx.entryStates[block] = ls.fork()
	}

	for _, instr := range block.Instrs {
		ctx.processInstruction(wctx.fn, instr, ls)
	}

	wctx.exitStates[block] = ls.fork()

	for _, succ := range block.Succs {
		ctx.walkBlock(wctx, succ, block, ls.fork())
	}
}

// isBackEdge returns true if the edge from→to is a back-edge in the CFG.
// A back-edge goes from a block to one of its dominators (forming a loop).
func isBackEdge(from, to *ssa.BasicBlock) bool {
	return to.Dominates(from)
}

// processInstruction dispatches on instruction type to track locks and record
// field access observations.
func (ctx *passContext) processInstruction(fn *ssa.Function, instr ssa.Instruction, ls *lockState) {
	switch inst := instr.(type) {
	case *ssa.Call:
		ctx.processCall(fn, inst, ls)
	case *ssa.Defer:
		// Deferred calls execute at function return (RunDefers), not here.
		// For golintmu's goal (detect inconsistent field access locking),
		// we intentionally do NOT modify lockState at defer sites:
		//   - defer mu.Unlock(): lock stays held through function body, so
		//     field accesses within the body are correctly seen as locked.
		//   - defer mu.Lock(): lock not acquired during function body, so
		//     field accesses are correctly seen as unlocked.
		// This simplified model is sufficient for guard inference.
		// However, we DO check for mismatched unlock mode (e.g. defer mu.Unlock()
		// after mu.RLock()) without modifying lock state.
		ctx.checkDeferredUnlockMismatch(fn, inst, ls)
		// Detect defer mu.Lock() typo (should be defer mu.Unlock()).
		ctx.checkDeferredLockInsteadOfUnlock(fn, inst)
		// Record deferred unlock for C5 lock-leak detection.
		ctx.recordDeferredUnlock(inst, ls)
	case *ssa.Return:
		ctx.checkReturnWithHeldLocks(fn, inst, ls)
	case *ssa.Store:
		ctx.processStore(fn, inst, ls)
	case *ssa.UnOp:
		ctx.processRead(fn, inst, ls)
	}
}

// processCall handles Lock/Unlock calls, updates lock state, records call sites,
// and detects intra-function double-locks.
func (ctx *passContext) processCall(fn *ssa.Function, call *ssa.Call, ls *lockState) {
	common := call.Common()
	if common.IsInvoke() {
		if common.Method == nil {
			return
		}
		recv := common.Value
		methodName := common.Method.Name()
		if isLockMethod(methodName) {
			ref := resolveLockRef(recv)
			if ref != nil {
				if isLockAcquire(methodName) {
					ctx.checkAndRecordLockAcquire(fn, call.Pos(), ref, isExclusiveLock(methodName), ls)
				} else {
					ctx.checkAndRecordUnlock(fn, call.Pos(), ref, isExclusiveUnlock(methodName), ls)
				}
			}
		}
		return
	}

	callee := common.StaticCallee()
	if callee == nil {
		return
	}

	recv := common.Args
	if len(recv) == 0 {
		return
	}

	methodName := callee.Name()
	if isLockMethod(methodName) {
		recvVal := recv[0]
		var ref *lockRef
		if isMutexReceiver(recvVal) {
			ref = resolveLockRef(recvVal)
		} else {
			ref = resolveEmbeddedMutexRef(recvVal, methodName)
		}
		if ref != nil {
			if isLockAcquire(methodName) {
				ctx.checkAndRecordLockAcquire(fn, call.Pos(), ref, isExclusiveLock(methodName), ls)
			} else {
				ctx.checkAndRecordUnlock(fn, call.Pos(), ref, isExclusiveUnlock(methodName), ls)
			}
		}
		return
	}

	// Non-lock static call: record call site for interprocedural analysis.
	var receiverVal ssa.Value
	if callee.Signature.Recv() != nil && len(common.Args) > 0 {
		receiverVal = common.Args[0]
	}
	ctx.recordCallSite(fn, callee, call.Pos(), ls, receiverVal)
}

// checkAndRecordLockAcquire checks for intra-function double-lock (including
// recursive RLock and lock upgrade), records the lock acquisition in funcFacts,
// then acquires the lock.
func (ctx *passContext) checkAndRecordLockAcquire(fn *ssa.Function, pos token.Pos, ref *lockRef, exclusive bool, ls *lockState) {
	// Check for double-lock: is this lock already held?
	if existing, alreadyHeld := ls.held[*ref]; alreadyHeld {
		if exclusive && existing.exclusive {
			// Lock-after-Lock: existing double-lock diagnostic.
			ctx.reportDoubleLock(fn, pos, ref)
		} else if !exclusive && !existing.exclusive {
			// RLock-after-RLock: recursive RLock — deadlock risk.
			ctx.reportRecursiveRLock(fn, pos, ref)
		} else if exclusive && !existing.exclusive {
			// Lock-after-RLock: lock upgrade attempt — deadlock.
			ctx.reportLockUpgradeAttempt(fn, pos, ref)
		} else {
			// RLock-after-Lock: already exclusively held.
			ctx.reportDoubleLock(fn, pos, ref)
		}
	}

	// Record lock-order edges: for each lock already held, add an edge held→acquired.
	// Skip when held and acquired are the same lock instance (double-lock, already C2).
	acquiredKey, acquiredOk := lockRefToMutexFieldKey(ref)
	if acquiredOk {
		for heldRef := range ls.held {
			if heldRef == *ref {
				continue // same instance — double-lock, not an ordering issue
			}
			heldKey, heldOk := lockRefToMutexFieldKey(&heldRef)
			if !heldOk {
				continue
			}
			ctx.lockOrderGraph.addEdge(lockOrderEdge{
				From: heldKey,
				To:   acquiredKey,
				Pos:  pos,
				Fn:   fn,
			})
		}
	}

	// Record the acquisition in funcFacts.
	ctx.recordLockAcquisition(fn, ref)

	// Actually acquire the lock.
	ls.lock(*ref, exclusive, pos)
}

// checkAndRecordUnlock checks for mismatched unlock (e.g. Unlock after RLock)
// and then releases the lock.
func (ctx *passContext) checkAndRecordUnlock(fn *ssa.Function, pos token.Pos, ref *lockRef, exclusiveUnlock bool, ls *lockState) {
	if existing, held := ls.held[*ref]; held {
		if existing.exclusive && !exclusiveUnlock {
			// Lock held exclusively, but RUnlock() called.
			ctx.reportMismatchedUnlock(fn, pos, ref, true, "RUnlock")
		} else if !existing.exclusive && exclusiveUnlock {
			// Lock held as shared (RLock), but Unlock() called.
			ctx.reportMismatchedUnlock(fn, pos, ref, false, "Unlock")
		}
	} else {
		// Lock not held — C4: unlock of unlocked mutex.
		// Defer reporting to Phase 3.3 so Requires facts are available for suppression.
		ctx.unlockOfUnlockedCandidates = append(ctx.unlockOfUnlockedCandidates,
			unlockOfUnlockedCandidate{Fn: fn, Pos: pos, Ref: *ref})
	}
	ls.unlock(*ref)

	// Record that this function releases this mutex.
	if mfk, ok := lockRefToMutexFieldKey(ref); ok {
		ctx.getOrCreateFuncFacts(fn).Releases[mfk] = true
	}
}

// resolveDeferredLockRef extracts the lockRef and method name from a deferred call.
// Returns nil if the deferred call is not a lock/unlock method.
func resolveDeferredLockRef(d *ssa.Defer) (*lockRef, string) {
	common := d.Common()

	var methodName string
	var recv ssa.Value

	if common.IsInvoke() {
		if common.Method == nil {
			return nil, ""
		}
		methodName = common.Method.Name()
		recv = common.Value
	} else {
		callee := common.StaticCallee()
		if callee == nil {
			return nil, ""
		}
		methodName = callee.Name()
		if len(common.Args) == 0 {
			return nil, ""
		}
		recv = common.Args[0]
	}

	if !isLockMethod(methodName) {
		return nil, ""
	}

	var ref *lockRef
	if isMutexReceiver(recv) {
		ref = resolveLockRef(recv)
	} else {
		ref = resolveEmbeddedMutexRef(recv, methodName)
	}
	return ref, methodName
}

// checkDeferredUnlockMismatch checks a deferred call for mismatched unlock mode
// without modifying lock state (preserving existing defer semantics).
func (ctx *passContext) checkDeferredUnlockMismatch(fn *ssa.Function, d *ssa.Defer, ls *lockState) {
	ref, methodName := resolveDeferredLockRef(d)
	if ref == nil || isLockAcquire(methodName) {
		return
	}

	existing, held := ls.held[*ref]
	if !held {
		return
	}

	exclusiveUnlock := isExclusiveUnlock(methodName)
	if existing.exclusive && !exclusiveUnlock {
		ctx.reportMismatchedUnlock(fn, d.Pos(), ref, true, "RUnlock")
	} else if !existing.exclusive && exclusiveUnlock {
		ctx.reportMismatchedUnlock(fn, d.Pos(), ref, false, "Unlock")
	}
}

// checkDeferredLockInsteadOfUnlock detects the typo `defer mu.Lock()` instead of
// `defer mu.Unlock()`. Fires when the deferred method is a lock acquire.
func (ctx *passContext) checkDeferredLockInsteadOfUnlock(fn *ssa.Function, d *ssa.Defer) {
	ref, methodName := resolveDeferredLockRef(d)
	if ref == nil || !isLockAcquire(methodName) {
		return
	}
	ctx.reportDeferredLockInsteadOfUnlock(fn, d.Pos(), ref, methodName)
	ctx.deferredLockTypoReported[deferredLockTypoKey{fn: fn, ref: *ref}] = true
}

// recordDeferredUnlock records a deferred unlock in the lock state for C5 leak detection.
func (ctx *passContext) recordDeferredUnlock(d *ssa.Defer, ls *lockState) {
	ref, methodName := resolveDeferredLockRef(d)
	if ref == nil || isLockAcquire(methodName) {
		return
	}
	ls.deferUnlock(*ref)

	// Record that this function releases this mutex.
	if mfk, ok := lockRefToMutexFieldKey(ref); ok {
		ctx.getOrCreateFuncFacts(d.Parent()).Releases[mfk] = true
	}
}

// checkReturnWithHeldLocks checks for locks held at a return point that are not
// covered by a deferred unlock. Collects candidates for deferred C5 reporting.
// Uses map keyed by return position to clear stale candidates on block re-walks.
func (ctx *passContext) checkReturnWithHeldLocks(fn *ssa.Function, ret *ssa.Return, ls *lockState) {
	retPos := ret.Pos()
	// Clear any candidates from previous walks of this return point.
	delete(ctx.lockLeakCandidates, retPos)

	var candidates []lockLeakCandidate
	for ref, hl := range ls.held {
		if ls.deferredUnlocks[ref] {
			continue
		}
		candidates = append(candidates, lockLeakCandidate{
			Fn:         fn,
			Pos:        retPos,
			Ref:        ref,
			AcquirePos: hl.pos,
		})
	}
	if len(candidates) > 0 {
		ctx.lockLeakCandidates[retPos] = candidates
	}
}

// recordCallSite records a static call with the normalized lock state at the call point.
func (ctx *passContext) recordCallSite(caller, callee *ssa.Function, pos token.Pos, ls *lockState, receiver ssa.Value) {
	cs := callSiteRecord{
		Caller:           caller,
		Callee:           callee,
		Pos:              pos,
		HeldByStructType: normalizeLockState(ls),
		ReceiverValue:    receiver,
	}
	ctx.callSites = append(ctx.callSites, cs)
}

// lockRefToMutexFieldKey normalizes a lockRef to a type-scoped mutexFieldKey.
// Returns false if the lockRef cannot be normalized (non-field lock or unresolvable type).
func lockRefToMutexFieldKey(ref *lockRef) (mutexFieldKey, bool) {
	if ref == nil || ref.kind != fieldLock {
		return mutexFieldKey{}, false
	}
	named, _, ok := resolveStructFromBase(ref.base)
	if !ok {
		return mutexFieldKey{}, false
	}
	return mutexFieldKey{StructType: named, FieldIndex: ref.fieldIndex}, true
}

// recordLockAcquisition records that a function directly acquires a lock.
func (ctx *passContext) recordLockAcquisition(fn *ssa.Function, ref *lockRef) {
	mfk, ok := lockRefToMutexFieldKey(ref)
	if !ok {
		return
	}
	facts := ctx.getOrCreateFuncFacts(fn)
	facts.Acquires[mfk] = true
}

// isMutexReceiver returns true if the value is a pointer to sync.Mutex or sync.RWMutex.
func isMutexReceiver(v ssa.Value) bool {
	t := v.Type()
	ptr, ok := t.(*types.Pointer)
	if !ok {
		return false
	}
	return isMutexType(ptr.Elem())
}

// processStore handles store instructions to record write observations.
func (ctx *passContext) processStore(fn *ssa.Function, store *ssa.Store, ls *lockState) {
	base, fieldIdx, structType, ok := resolveFieldAccess(store.Addr)
	if !ok {
		return
	}
	st, stOk := structType.Underlying().(*types.Struct)
	if !stOk || fieldIdx >= st.NumFields() {
		return
	}
	if isMutexType(st.Field(fieldIdx).Type()) {
		return
	}

	key := fieldKey{StructType: structType, FieldIndex: fieldIdx}
	ok2 := obsKey{field: key, pos: store.Pos(), isRead: false}
	if ctx.observedAt[ok2] {
		return
	}
	ctx.observedAt[ok2] = true
	obs := observation{
		SameBaseMutexFields: sameBaseMutexFields(base, ls),
		IsRead:              false,
		Func:                fn,
		Pos:                 store.Pos(),
	}
	ctx.observations[key] = append(ctx.observations[key], obs)

	// Record ancestor observations for value-type nested fields.
	if fa, isFA := store.Addr.(*ssa.FieldAddr); isFA {
		ctx.recordAncestorObservations(fn, fa, false, store.Pos(), ls)
	}
}

// processRead handles UnOp (dereference) instructions to record read observations.
func (ctx *passContext) processRead(fn *ssa.Function, unop *ssa.UnOp, ls *lockState) {
	base, fieldIdx, structType, ok := resolveFieldAccess(unop.X)
	if !ok {
		return
	}
	st, stOk := structType.Underlying().(*types.Struct)
	if !stOk || fieldIdx >= st.NumFields() {
		return
	}
	if isMutexType(st.Field(fieldIdx).Type()) {
		return
	}

	key := fieldKey{StructType: structType, FieldIndex: fieldIdx}
	ok2 := obsKey{field: key, pos: unop.Pos(), isRead: true}
	if ctx.observedAt[ok2] {
		return
	}
	ctx.observedAt[ok2] = true
	obs := observation{
		SameBaseMutexFields: sameBaseMutexFields(base, ls),
		IsRead:              true,
		Func:                fn,
		Pos:                 unop.Pos(),
	}
	ctx.observations[key] = append(ctx.observations[key], obs)

	// Record ancestor observations for value-type nested fields.
	if fa, isFA := unop.X.(*ssa.FieldAddr); isFA {
		ctx.recordAncestorObservations(fn, fa, true, unop.Pos(), ls)
	}
}

// recordAncestorObservations walks up the FieldAddr chain to detect value-type
// ancestor fields and records additional observations for each. This handles
// the case where accessing c.state.Status (where state is a value type) should
// also record an observation for Container.state, since no intermediate UnOp
// is produced for value-type field chains.
func (ctx *passContext) recordAncestorObservations(fn *ssa.Function, primaryFA *ssa.FieldAddr, isRead bool, pos token.Pos, ls *lockState) {
	seen := make(map[ssa.Value]bool)
	current := primaryFA.X
	for !seen[current] {
		seen[current] = true
		unwrapped := unwrapSSAValue(current)
		ancestorFA, isFA := unwrapped.(*ssa.FieldAddr)
		if !isFA {
			break
		}

		base, fieldIdx, structType, ok := resolveFieldAccess(unwrapped)
		if !ok {
			break
		}
		st, stOk := structType.Underlying().(*types.Struct)
		if !stOk || fieldIdx >= st.NumFields() {
			break
		}
		if isMutexType(st.Field(fieldIdx).Type()) {
			break
		}

		key := fieldKey{StructType: structType, FieldIndex: fieldIdx}
		ok2 := obsKey{field: key, pos: pos, isRead: isRead}
		if !ctx.observedAt[ok2] {
			ctx.observedAt[ok2] = true
			ctx.observations[key] = append(ctx.observations[key], observation{
				SameBaseMutexFields: sameBaseMutexFields(base, ls),
				IsRead:              isRead,
				Func:                fn,
				Pos:                 pos,
			})
		}

		current = ancestorFA.X
	}
}

// sameBaseMutexFields returns the held mutex fields whose base SSA value
// matches the given base. This identifies which mutexes on the same struct
// instance are held at this program point, including their lock mode.
func sameBaseMutexFields(base ssa.Value, ls *lockState) []heldMutexField {
	var fields []heldMutexField
	for _, hl := range ls.held {
		if hl.ref.base == base {
			fields = append(fields, heldMutexField{
				FieldIndex: hl.ref.fieldIndex,
				Exclusive:  hl.exclusive,
			})
		}
	}
	return fields
}
