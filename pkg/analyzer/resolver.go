package analyzer

import (
	"go/types"

	"golang.org/x/tools/go/ssa"
)

// resolveLockRef traces an SSA value back to its origin and returns a lockRef
// identifying the specific lock instance. Returns nil if the value cannot be
// resolved to a known lock.
func resolveLockRef(v ssa.Value) *lockRef {
	// Unwrap pointer indirections and copies.
	v = unwrapSSAValue(v)

	fa, ok := v.(*ssa.FieldAddr)
	if !ok {
		return nil
	}
	// Check that the field is a mutex type.
	structType := fa.X.Type().Underlying().(*types.Pointer).Elem().Underlying().(*types.Struct)
	field := structType.Field(fa.Field)
	if !isMutexType(field.Type()) {
		return nil
	}
	base := unwrapSSAValue(fa.X)
	return &lockRef{
		kind:       fieldLock,
		base:       base,
		fieldIndex: fa.Field,
	}
}

// unwrapSSAValue strips Phi nodes (if all edges agree), UnOp (dereference),
// and other pass-through SSA values to find the underlying value.
func unwrapSSAValue(v ssa.Value) ssa.Value {
	for {
		switch val := v.(type) {
		case *ssa.Phi:
			// If all phi edges agree on the same value, unwrap.
			if resolved := resolvePhiIfUniform(val); resolved != nil {
				v = resolved
				continue
			}
			return v
		default:
			return v
		}
	}
}

// resolvePhiIfUniform returns the single unique value if all phi edges agree,
// or nil if they diverge.
func resolvePhiIfUniform(phi *ssa.Phi) ssa.Value {
	var unique ssa.Value
	for _, edge := range phi.Edges {
		edge = unwrapSSAValue(edge)
		if unique == nil {
			unique = edge
		} else if unique != edge {
			return nil
		}
	}
	return unique
}

// isMutexType returns true if the type is sync.Mutex or sync.RWMutex.
func isMutexType(t types.Type) bool {
	named, ok := t.(*types.Named)
	if !ok {
		return false
	}
	obj := named.Obj()
	if obj.Pkg() == nil || obj.Pkg().Path() != "sync" {
		return false
	}
	return obj.Name() == "Mutex" || obj.Name() == "RWMutex"
}

// isLockMethod returns true if the method name is a lock/unlock operation.
func isLockMethod(name string) bool {
	switch name {
	case "Lock", "Unlock", "RLock", "RUnlock":
		return true
	}
	return false
}

// isLockAcquire returns true if the method acquires a lock.
func isLockAcquire(name string) bool {
	return name == "Lock" || name == "RLock"
}

// resolveFieldAccess extracts the struct type and field index from a FieldAddr
// instruction. Returns the base SSA value, field index, named struct type, and
// whether the extraction succeeded.
func resolveFieldAccess(v ssa.Value) (base ssa.Value, fieldIdx int, structType *types.Named, ok bool) {
	fa, isFA := v.(*ssa.FieldAddr)
	if !isFA {
		return nil, 0, nil, false
	}

	// The base of FieldAddr is a pointer to the struct.
	ptrType, isPtrType := fa.X.Type().Underlying().(*types.Pointer)
	if !isPtrType {
		return nil, 0, nil, false
	}
	named, isNamed := ptrType.Elem().(*types.Named)
	if !isNamed {
		return nil, 0, nil, false
	}

	return unwrapSSAValue(fa.X), fa.Field, named, true
}
