package analyzer

import (
	"go/types"
	"strings"

	"golang.org/x/tools/go/ssa"
)

// inferGuards runs guard inference for all candidate struct fields.
func (ctx *passContext) inferGuards() {
	for key, observations := range ctx.observations {
		// Filter out constructor observations.
		var filtered []observation
		for _, obs := range observations {
			if isConstructorLike(obs.Func, key.StructType) {
				continue
			}
			filtered = append(filtered, obs)
		}
		if len(filtered) == 0 {
			continue
		}

		// Check if the field is immutable (all writes in constructors).
		if isImmutableField(filtered) {
			continue
		}

		// Check if any observation has a lock held — if so, infer the guard.
		guard, ok := inferFieldGuard(key, filtered)
		if ok {
			ctx.guards[key] = guard
		}
	}
}

// isConstructorLike returns true if the function looks like a constructor for
// the given struct type.
func isConstructorLike(fn *ssa.Function, structType *types.Named) bool {
	if fn.Name() == "init" {
		return true
	}

	if returnsStructType(fn, structType) {
		return true
	}

	// Name-based heuristic: only match New/Make/Create prefixes when the
	// struct name is part of the function name (e.g. NewConfig for Config).
	structName := structType.Obj().Name()
	name := fn.Name()
	for _, prefix := range []string{"New", "Make", "Create"} {
		if strings.HasPrefix(name, prefix) && strings.Contains(name, structName) {
			return true
		}
	}

	return false
}

// returnsStructType returns true if fn's signature returns the given struct
// type (or a pointer to it).
func returnsStructType(fn *ssa.Function, structType *types.Named) bool {
	results := fn.Signature.Results()
	for i := 0; i < results.Len(); i++ {
		rt := results.At(i).Type()
		if types.Identical(rt, structType) {
			return true
		}
		if ptr, ok := rt.(*types.Pointer); ok {
			if types.Identical(ptr.Elem(), structType) {
				return true
			}
		}
	}
	return false
}

// isImmutableField returns true if all non-constructor observations are reads.
func isImmutableField(filteredObs []observation) bool {
	for _, obs := range filteredObs {
		if !obs.IsRead {
			return false
		}
	}
	return true
}

// inferFieldGuard examines observations and infers which mutex guards the field.
func inferFieldGuard(key fieldKey, observations []observation) (guardInfo, bool) {
	// Count how often each mutex field index appears as held.
	counts := make(map[int]int)

	for _, obs := range observations {
		for _, mutexFieldIdx := range obs.SameBaseMutexFields {
			// Self-exclusion: a mutex field is never guarded by itself.
			if mutexFieldIdx == key.FieldIndex {
				continue
			}
			counts[mutexFieldIdx]++
		}
	}

	if len(counts) == 0 {
		return guardInfo{}, false
	}

	// Pick the mutex held most often. Break ties by lowest field index for
	// deterministic results across runs.
	best := -1
	var bestCount int
	for fieldIdx, count := range counts {
		if count > bestCount || (count == bestCount && (best == -1 || fieldIdx < best)) {
			best = fieldIdx
			bestCount = count
		}
	}

	return guardInfo{MutexFieldIndex: best}, true
}
