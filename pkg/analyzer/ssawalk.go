package analyzer

import (
	"go/types"

	"golang.org/x/tools/go/ssa"
)

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
	ls := newLockState()
	visited := make(map[*ssa.BasicBlock]bool)
	ctx.walkBlock(fn, fn.Blocks[0], ls, visited)
}

// walkBlock processes all instructions in a basic block and recurses into successors.
func (ctx *passContext) walkBlock(fn *ssa.Function, block *ssa.BasicBlock, ls *lockState, visited map[*ssa.BasicBlock]bool) {
	if visited[block] {
		return
	}
	visited[block] = true

	for _, instr := range block.Instrs {
		ctx.processInstruction(fn, instr, ls)
	}

	for _, succ := range block.Succs {
		ctx.walkBlock(fn, succ, ls.clone(), visited)
	}
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
		// checks (lock leak detection C5, defer mu.Lock() typo C7) will
		// require handling RunDefers instructions at function exit points.
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
