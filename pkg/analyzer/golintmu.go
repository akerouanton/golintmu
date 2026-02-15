package analyzer

import (
	"go/token"
	"go/types"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/passes/buildssa"
	"golang.org/x/tools/go/ssa"
)

var Analyzer = &analysis.Analyzer{
	Name:     "golintmu",
	Doc:      "detects inconsistent mutex locking of struct fields",
	Run:      run,
	Requires: []*analysis.Analyzer{buildssa.Analyzer},
}

// fieldKey uniquely identifies a struct field across the package.
type fieldKey struct {
	StructType *types.Named
	FieldIndex int
}

// observation records a single field access with its lock state context.
// SameBaseMutexFields lists mutex field indices that were held on the same
// struct instance at the time of this access (e.g. if s.mu is held when
// accessing s.count, SameBaseMutexFields contains mu's field index).
type observation struct {
	SameBaseMutexFields []int
	IsRead              bool
	Func                *ssa.Function
	Pos                 token.Pos
}

// guardInfo records the inferred guard for a field.
type guardInfo struct {
	MutexFieldIndex int
}

// passContext holds state for a single analyzer pass.
type passContext struct {
	pass         *analysis.Pass
	ssaPkg       *ssa.Package
	srcFuncs     []*ssa.Function
	observations map[fieldKey][]observation
	guards       map[fieldKey]guardInfo
}

func run(pass *analysis.Pass) (interface{}, error) {
	ssaResult := pass.ResultOf[buildssa.Analyzer].(*buildssa.SSA)

	ctx := &passContext{
		pass:         pass,
		ssaPkg:       ssaResult.Pkg,
		srcFuncs:     ssaResult.SrcFuncs,
		observations: make(map[fieldKey][]observation),
		guards:       make(map[fieldKey]guardInfo),
	}

	// Phase 1: Collect observations by walking SSA.
	ctx.collectObservations()

	// Phase 2: Infer guards from observations.
	ctx.inferGuards()

	// Phase 3 (MVP skip): No interprocedural analysis yet.

	// Phase 4: Check violations.
	ctx.checkViolations()

	return nil, nil
}
