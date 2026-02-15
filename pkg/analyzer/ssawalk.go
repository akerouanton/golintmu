package analyzer

import (
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
			ctx.reportInconsistentLockState(block, prevEntry, ls)
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
		// This simplified model is sufficient for guard inference. Future
		// checks (lock leak detection, defer mu.Lock() typo) will require
		// handling RunDefers instructions at function exit points.
	case *ssa.Store:
		ctx.processStore(fn, inst, ls)
	case *ssa.UnOp:
		ctx.processRead(fn, inst, ls)
	}
}

// processCall handles Lock/Unlock calls and updates lock state.
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
					ls.lock(*ref, true)
				} else {
					ls.unlock(*ref)
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
	if !isLockMethod(methodName) {
		return
	}

	recvVal := recv[0]
	if !isMutexReceiver(recvVal) {
		return
	}

	ref := resolveLockRef(recvVal)
	if ref != nil {
		if isLockAcquire(methodName) {
			ls.lock(*ref, true)
		} else {
			ls.unlock(*ref)
		}
	}
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

// sameBaseMutexFields returns the field indices of held mutex locks whose base
// SSA value matches the given base. This identifies which mutexes on the same
// struct instance are held at this program point.
func sameBaseMutexFields(base ssa.Value, ls *lockState) []int {
	var fields []int
	for _, hl := range ls.held {
		if hl.ref.base == base {
			fields = append(fields, hl.ref.fieldIndex)
		}
	}
	return fields
}
