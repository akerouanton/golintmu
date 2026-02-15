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
	ctx.recordCallSite(fn, callee, call.Pos(), ls)
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

	// Record the acquisition in funcFacts.
	ctx.recordLockAcquisition(fn, ref)

	// Actually acquire the lock.
	ls.lock(*ref, exclusive)
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
	}
	ls.unlock(*ref)
}

// checkDeferredUnlockMismatch checks a deferred call for mismatched unlock mode
// without modifying lock state (preserving existing defer semantics).
func (ctx *passContext) checkDeferredUnlockMismatch(fn *ssa.Function, d *ssa.Defer, ls *lockState) {
	common := d.Common()

	var methodName string
	var recv ssa.Value

	if common.IsInvoke() {
		if common.Method == nil {
			return
		}
		methodName = common.Method.Name()
		recv = common.Value
	} else {
		callee := common.StaticCallee()
		if callee == nil {
			return
		}
		methodName = callee.Name()
		if len(common.Args) == 0 {
			return
		}
		recv = common.Args[0]
	}

	if !isLockMethod(methodName) || isLockAcquire(methodName) {
		return
	}

	var ref *lockRef
	if isMutexReceiver(recv) {
		ref = resolveLockRef(recv)
	} else {
		ref = resolveEmbeddedMutexRef(recv, methodName)
	}
	if ref == nil {
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

// recordCallSite records a static call with the normalized lock state at the call point.
func (ctx *passContext) recordCallSite(caller, callee *ssa.Function, pos token.Pos, ls *lockState) {
	cs := callSiteRecord{
		Caller:           caller,
		Callee:           callee,
		Pos:              pos,
		HeldByStructType: normalizeLockState(ls),
	}
	ctx.callSites = append(ctx.callSites, cs)
}

// recordLockAcquisition records that a function directly acquires a lock.
func (ctx *passContext) recordLockAcquisition(fn *ssa.Function, ref *lockRef) {
	if ref.kind != fieldLock {
		return
	}
	ptrType, ok := ref.base.Type().Underlying().(*types.Pointer)
	if !ok {
		return
	}
	named, ok := ptrType.Elem().(*types.Named)
	if !ok {
		return
	}
	mfk := mutexFieldKey{StructType: named, FieldIndex: ref.fieldIndex}
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
