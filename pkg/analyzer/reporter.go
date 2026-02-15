package analyzer

import (
	"fmt"
	"go/types"
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
