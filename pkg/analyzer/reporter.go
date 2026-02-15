package analyzer

import (
	"fmt"
	"go/token"
	"go/types"

	"golang.org/x/tools/go/ssa"
)

// checkViolations re-walks observations and reports fields accessed without
// their inferred guard lock held.
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
				ctx.reportViolation(obs, key, guard)
			}
		}
	}
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
