package analyzer

import (
	"go/types"
	"strings"

	"golang.org/x/tools/go/ssa"
)

// inferGuards runs guard inference for all candidate struct fields.
func (ctx *passContext) inferGuards() {
	for key, observations := range ctx.observations {
		// Skip imported types — their guards came from facts.
		if key.StructType.Obj().Pkg() != ctx.pass.Pkg {
			continue
		}

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
	// SSA renames user-written init() functions to init#1, init#2, etc.
	if fn.Name() == "init" || strings.HasPrefix(fn.Name(), "init#") {
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
// It also computes NeedsExclusive: true when any observation is a write under
// the inferred guard.
func inferFieldGuard(key fieldKey, observations []observation) (guardInfo, bool) {
	// Phase 1: Count mutex frequency from WRITE observations only.
	// Writes are the authoritative signal — the mutex held during writes is
	// overwhelmingly the actual guard; reads under a different lock are
	// coincidental (e.g. tandem-lock patterns).
	best := pickMostFrequentMutex(key, observations, true)

	// Phase 2 (fallback): If no writes held a mutex, use ALL observations.
	// This handles read-only fields or fields never written under a lock.
	if best == -1 {
		best = pickMostFrequentMutex(key, observations, false)
	}

	if best == -1 {
		return guardInfo{}, false
	}

	// Compute NeedsExclusive: true when any observation is a write under the guard.
	needsExclusive := false
	for _, obs := range observations {
		if obs.IsRead {
			continue
		}
		for _, hmf := range obs.SameBaseMutexFields {
			if hmf.FieldIndex == best {
				needsExclusive = true
				break
			}
		}
		if needsExclusive {
			break
		}
	}

	return guardInfo{MutexFieldIndex: best, NeedsExclusive: needsExclusive}, true
}

// pickMostFrequentMutex counts how often each mutex field index appears as held
// across the given observations and returns the most frequent one. If writesOnly
// is true, only write observations are considered. Returns -1 if no mutex was found.
func pickMostFrequentMutex(key fieldKey, observations []observation, writesOnly bool) int {
	counts := make(map[int]int)
	for _, obs := range observations {
		if writesOnly && obs.IsRead {
			continue
		}
		for _, hmf := range obs.SameBaseMutexFields {
			if hmf.FieldIndex == key.FieldIndex {
				continue
			}
			counts[hmf.FieldIndex]++
		}
	}

	best := -1
	var bestCount int
	for fieldIdx, count := range counts {
		if count > bestCount || (count == bestCount && (best == -1 || fieldIdx < best)) {
			best = fieldIdx
			bestCount = count
		}
	}
	return best
}
