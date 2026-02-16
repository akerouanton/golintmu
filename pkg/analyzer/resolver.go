package analyzer

import (
	"go/token"
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
	ptr, ok := fa.X.Type().Underlying().(*types.Pointer)
	if !ok {
		return nil
	}
	structType, ok := ptr.Elem().Underlying().(*types.Struct)
	if !ok {
		return nil
	}
	if fa.Field >= structType.NumFields() {
		return nil
	}
	field := structType.Field(fa.Field)
	if !isMutexType(field.Type()) {
		return nil
	}
	base := canonicalizeBase(fa.X)
	return &lockRef{
		kind:       fieldLock,
		base:       base,
		fieldIndex: fa.Field,
	}
}

// unwrapSSAValue strips Phi nodes (if all edges agree) to find the underlying value.
func unwrapSSAValue(v ssa.Value) ssa.Value {
	visited := make(map[*ssa.Phi]bool)
	return unwrapSSAValueVisited(v, visited)
}

// canonicalizeBase returns a canonical SSA value for use as a lockRef base.
// It follows through UnOp dereferences (token.MUL) in addition to Phi nodes.
// This is needed because when a closure captures a variable, the SSA builder
// "lifts" it to a heap-allocated cell. Each use of the variable becomes a
// separate load (UnOp deref) from the cell, producing different SSA values for
// the same logical variable. By following through the deref to the underlying
// Alloc, two loads from the same cell resolve to the same canonical value.
func canonicalizeBase(v ssa.Value) ssa.Value {
	v = unwrapSSAValue(v)
	seen := make(map[ssa.Value]bool)
	for {
		if seen[v] {
			return v
		}
		seen[v] = true
		unop, ok := v.(*ssa.UnOp)
		if !ok || unop.Op != token.MUL {
			return v
		}
		v = unwrapSSAValue(unop.X)
	}
}

func unwrapSSAValueVisited(v ssa.Value, visited map[*ssa.Phi]bool) ssa.Value {
	for {
		switch val := v.(type) {
		case *ssa.Phi:
			// If all phi edges agree on the same value, unwrap.
			if resolved := resolvePhiIfUniform(val, visited); resolved != nil {
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
// or nil if they diverge. The visited set prevents infinite recursion on phi
// cycles (common in loops).
func resolvePhiIfUniform(phi *ssa.Phi, visited map[*ssa.Phi]bool) ssa.Value {
	if visited[phi] {
		return nil
	}
	visited[phi] = true

	var unique ssa.Value
	for _, edge := range phi.Edges {
		edge = unwrapSSAValueVisited(edge, visited)
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
	if obj == nil || obj.Pkg() == nil || obj.Pkg().Path() != "sync" {
		return false
	}
	return obj.Name() == "Mutex" || obj.Name() == "RWMutex"
}

// isRWMutexType returns true if the type is sync.RWMutex specifically.
func isRWMutexType(t types.Type) bool {
	named, ok := t.(*types.Named)
	if !ok {
		return false
	}
	obj := named.Obj()
	return obj != nil && obj.Pkg() != nil && obj.Pkg().Path() == "sync" && obj.Name() == "RWMutex"
}

// isRWLockMethod returns true if the method is RLock or RUnlock.
func isRWLockMethod(name string) bool {
	return name == "RLock" || name == "RUnlock"
}

// resolveEmbeddedMutexRef handles the wrapper-call case where the receiver is
// a pointer to a struct that embeds sync.Mutex or sync.RWMutex. SSA can
// generate (*S).Lock(s) calls where the receiver is *S, not *sync.Mutex.
func resolveEmbeddedMutexRef(recv ssa.Value, methodName string) *lockRef {
	recv = unwrapSSAValue(recv)
	ptrType, ok := recv.Type().Underlying().(*types.Pointer)
	if !ok {
		return nil
	}
	structType, ok := ptrType.Elem().Underlying().(*types.Struct)
	if !ok {
		return nil
	}
	for i := 0; i < structType.NumFields(); i++ {
		field := structType.Field(i)
		if !field.Anonymous() || !isMutexType(field.Type()) {
			continue
		}
		// RLock/RUnlock require sync.RWMutex specifically.
		if isRWLockMethod(methodName) && !isRWMutexType(field.Type()) {
			continue
		}
		return &lockRef{kind: fieldLock, base: canonicalizeBase(recv), fieldIndex: i}
	}
	return nil
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

// isExclusiveLock returns true if the method acquires an exclusive lock.
func isExclusiveLock(name string) bool {
	return name == "Lock"
}

// isExclusiveUnlock returns true if the method releases an exclusive lock.
func isExclusiveUnlock(name string) bool {
	return name == "Unlock"
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

	return canonicalizeBase(fa.X), fa.Field, named, true
}
