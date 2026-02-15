package analyzer

import (
	"fmt"
	"go/types"
	"sort"
	"strings"

	"golang.org/x/tools/go/ssa"
)

// FieldGuardFact is exported as an analysis.Fact attached to *types.TypeName.
// It records which fields of a struct are guarded by which mutex fields.
type FieldGuardFact struct {
	Guards map[int]int // fieldIndex → mutexFieldIndex
}

func (*FieldGuardFact) AFact() {}

func (f *FieldGuardFact) String() string {
	// Produce sorted output for determinism.
	keys := make([]int, 0, len(f.Guards))
	for k := range f.Guards {
		keys = append(keys, k)
	}
	sort.Ints(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = fmt.Sprintf("%d->%d", k, f.Guards[k])
	}
	return fmt.Sprintf("FieldGuardFact{%s}", strings.Join(parts, " "))
}

// MutexRef is a gob-encodable reference to a mutex field.
type MutexRef struct {
	PkgPath    string
	TypeName   string
	FieldIndex int
}

// FuncLockFact is exported as an analysis.Fact attached to *types.Func.
// It records lock requirements and acquisitions for a function.
type FuncLockFact struct {
	Requires           []MutexRef
	Acquires           []MutexRef
	AcquiresTransitive []MutexRef
}

func (*FuncLockFact) AFact() {}

func (f *FuncLockFact) String() string {
	fmtRefs := func(refs []MutexRef) string {
		if len(refs) == 0 {
			return "[]"
		}
		parts := make([]string, len(refs))
		for i, r := range refs {
			parts[i] = fmt.Sprintf("%s.%d", r.TypeName, r.FieldIndex)
		}
		return "[" + strings.Join(parts, " ") + "]"
	}
	return fmt.Sprintf("FuncLockFact{requires=%s acquires=%s}", fmtRefs(f.Requires), fmtRefs(f.Acquires))
}

// ConcurrentFact is exported as an analysis.Fact attached to *types.Func.
// It marks a function as a concurrent entrypoint.
type ConcurrentFact struct{}

func (*ConcurrentFact) AFact() {}

func (*ConcurrentFact) String() string { return "ConcurrentFact" }

// mutexFieldKeyToRef converts an internal mutexFieldKey to a serializable MutexRef.
func mutexFieldKeyToRef(mfk mutexFieldKey) MutexRef {
	return MutexRef{
		PkgPath:    mfk.StructType.Obj().Pkg().Path(),
		TypeName:   mfk.StructType.Obj().Name(),
		FieldIndex: mfk.FieldIndex,
	}
}

// mutexRefToKey resolves a serializable MutexRef back to an internal mutexFieldKey.
func (ctx *passContext) mutexRefToKey(ref MutexRef) (mutexFieldKey, bool) {
	var pkg *types.Package
	if ref.PkgPath == ctx.pass.Pkg.Path() {
		pkg = ctx.pass.Pkg
	} else {
		for _, imp := range ctx.pass.Pkg.Imports() {
			if imp.Path() == ref.PkgPath {
				pkg = imp
				break
			}
		}
	}
	if pkg == nil {
		return mutexFieldKey{}, false
	}

	obj := pkg.Scope().Lookup(ref.TypeName)
	if obj == nil {
		return mutexFieldKey{}, false
	}
	typeName, ok := obj.(*types.TypeName)
	if !ok {
		return mutexFieldKey{}, false
	}
	named, ok := typeName.Type().(*types.Named)
	if !ok {
		return mutexFieldKey{}, false
	}

	// Validate field index against actual struct size.
	st, ok := named.Underlying().(*types.Struct)
	if !ok || ref.FieldIndex >= st.NumFields() {
		return mutexFieldKey{}, false
	}

	return mutexFieldKey{
		StructType: named,
		FieldIndex: ref.FieldIndex,
	}, true
}

// mutexFieldKeySetToRefs converts a set of mutexFieldKeys to a sorted slice of MutexRefs.
func mutexFieldKeySetToRefs(set map[mutexFieldKey]bool) []MutexRef {
	refs := make([]MutexRef, 0, len(set))
	for mfk := range set {
		refs = append(refs, mutexFieldKeyToRef(mfk))
	}
	sort.Slice(refs, func(i, j int) bool {
		if refs[i].PkgPath != refs[j].PkgPath {
			return refs[i].PkgPath < refs[j].PkgPath
		}
		if refs[i].TypeName != refs[j].TypeName {
			return refs[i].TypeName < refs[j].TypeName
		}
		return refs[i].FieldIndex < refs[j].FieldIndex
	})
	return refs
}

// importFacts imports upstream facts for types and functions used in this package.
// Skipped when the analyzer has no registered FactTypes (e.g. single-package tests).
func (ctx *passContext) importFacts() {
	if len(ctx.pass.Analyzer.FactTypes) == 0 {
		return
	}
	ctx.importFieldGuardFacts()
	ctx.importFuncLockFacts()
	ctx.importConcurrentFacts()
}

// importFieldGuardFacts imports FieldGuardFact for imported struct types that
// appear in observations.
func (ctx *passContext) importFieldGuardFacts() {
	seen := make(map[*types.Named]bool)
	for key := range ctx.observations {
		if key.StructType.Obj().Pkg() == ctx.pass.Pkg {
			continue
		}
		if seen[key.StructType] {
			continue
		}
		seen[key.StructType] = true

		var fact FieldGuardFact
		if !ctx.pass.ImportObjectFact(key.StructType.Obj(), &fact) {
			continue
		}

		for fieldIndex, mutexFieldIndex := range fact.Guards {
			fk := fieldKey{
				StructType: key.StructType,
				FieldIndex: fieldIndex,
			}
			ctx.guards[fk] = guardInfo{MutexFieldIndex: mutexFieldIndex}
		}
	}
}

// importFuncLockFacts imports FuncLockFact for imported callees.
func (ctx *passContext) importFuncLockFacts() {
	seen := make(map[*ssa.Function]bool)
	for _, cs := range ctx.callSites {
		callee := cs.Callee
		if callee.Object() == nil {
			continue
		}
		if callee.Object().Pkg() == ctx.pass.Pkg {
			continue
		}
		if seen[callee] {
			continue
		}
		seen[callee] = true

		var fact FuncLockFact
		if !ctx.pass.ImportObjectFact(callee.Object(), &fact) {
			continue
		}

		facts := ctx.getOrCreateFuncFacts(callee)
		for _, ref := range fact.Requires {
			if mfk, ok := ctx.mutexRefToKey(ref); ok {
				facts.Requires[mfk] = true
			}
		}
		for _, ref := range fact.Acquires {
			if mfk, ok := ctx.mutexRefToKey(ref); ok {
				facts.Acquires[mfk] = true
			}
		}
		for _, ref := range fact.AcquiresTransitive {
			if mfk, ok := ctx.mutexRefToKey(ref); ok {
				facts.AcquiresTransitive[mfk] = true
			}
		}
	}
}

// importConcurrentFacts imports ConcurrentFact for imported callees and merges
// them into the annotation concurrent set.
func (ctx *passContext) importConcurrentFacts() {
	seen := make(map[*ssa.Function]bool)
	for _, cs := range ctx.callSites {
		callee := cs.Callee
		if callee.Object() == nil {
			continue
		}
		if callee.Object().Pkg() == ctx.pass.Pkg {
			continue
		}
		if seen[callee] {
			continue
		}
		seen[callee] = true

		var fact ConcurrentFact
		if ctx.pass.ImportObjectFact(callee.Object(), &fact) {
			if ctx.annotations != nil {
				ctx.annotations.concurrent[callee] = true
			}
		}
	}
}

// exportFacts exports facts for types and functions defined in this package.
// Skipped when the analyzer has no registered FactTypes (e.g. single-package tests).
func (ctx *passContext) exportFacts() {
	if len(ctx.pass.Analyzer.FactTypes) == 0 {
		return
	}
	ctx.exportFieldGuardFacts()
	ctx.exportFuncLockFacts()
	ctx.exportConcurrentFacts()
}

// exportFieldGuardFacts groups guards by struct type and exports FieldGuardFact
// for exported types.
func (ctx *passContext) exportFieldGuardFacts() {
	byType := make(map[*types.Named]map[int]int)
	for key, guard := range ctx.guards {
		if key.StructType.Obj().Pkg() != ctx.pass.Pkg {
			continue
		}
		if !key.StructType.Obj().Exported() {
			continue
		}
		if byType[key.StructType] == nil {
			byType[key.StructType] = make(map[int]int)
		}
		byType[key.StructType][key.FieldIndex] = guard.MutexFieldIndex
	}

	for named, guards := range byType {
		ctx.pass.ExportObjectFact(named.Obj(), &FieldGuardFact{Guards: guards})
	}
}

// exportFuncLockFacts exports FuncLockFact for exported functions with lock
// requirements or acquisitions.
func (ctx *passContext) exportFuncLockFacts() {
	for fn, facts := range ctx.funcFacts {
		if fn.Object() == nil {
			continue
		}
		if fn.Object().Pkg() != ctx.pass.Pkg {
			continue
		}
		if !fn.Object().Exported() {
			continue
		}
		if len(facts.Requires) == 0 && len(facts.Acquires) == 0 && len(facts.AcquiresTransitive) == 0 {
			continue
		}

		ctx.pass.ExportObjectFact(fn.Object(), &FuncLockFact{
			Requires:           mutexFieldKeySetToRefs(facts.Requires),
			Acquires:           mutexFieldKeySetToRefs(facts.Acquires),
			AcquiresTransitive: mutexFieldKeySetToRefs(facts.AcquiresTransitive),
		})
	}
}

// exportConcurrentFacts exports ConcurrentFact for exported concurrent entrypoints.
func (ctx *passContext) exportConcurrentFacts() {
	entrypoints := ctx.detectConcurrentEntrypoints()
	for fn := range entrypoints {
		if fn.Object() == nil {
			continue
		}
		if fn.Object().Pkg() != ctx.pass.Pkg {
			continue
		}
		if !fn.Object().Exported() {
			continue
		}
		ctx.pass.ExportObjectFact(fn.Object(), &ConcurrentFact{})
	}
}
