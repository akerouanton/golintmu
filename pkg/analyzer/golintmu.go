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

// obsKey uniquely identifies an observation by field, source position, and
// access type, used to deduplicate observations when blocks are re-walked.
type obsKey struct {
	field  fieldKey
	pos    token.Pos
	isRead bool
}

// passContext holds state for a single analyzer pass.
type passContext struct {
	pass         *analysis.Pass
	ssaPkg       *ssa.Package
	srcFuncs     []*ssa.Function
	observations map[fieldKey][]observation
	guards       map[fieldKey]guardInfo
	observedAt   map[obsKey]bool // deduplication set for observations

	// Interprocedural analysis state.
	callSites []callSiteRecord
	funcFacts map[*ssa.Function]*funcLockFacts

	// Concurrency analysis state.
	// nil means "no entrypoints detected, treat all as concurrent".
	// Non-nil maps functions reachable from concurrent entrypoints.
	concurrentFuncs map[*ssa.Function]bool

	// Annotation directives parsed from comments.
	annotations *annotations
}

func run(pass *analysis.Pass) (any, error) {
	ssaResult, ok := pass.ResultOf[buildssa.Analyzer].(*buildssa.SSA)
	if !ok {
		return nil, nil
	}

	ctx := &passContext{
		pass:         pass,
		ssaPkg:       ssaResult.Pkg,
		srcFuncs:     ssaResult.SrcFuncs,
		observations: make(map[fieldKey][]observation),
		guards:       make(map[fieldKey]guardInfo),
		observedAt:   make(map[obsKey]bool),
		funcFacts:    make(map[*ssa.Function]*funcLockFacts),
	}

	// Phase 0: Parse annotation directives from comments.
	ctx.parseAnnotations()

	// Phase 1: Collect observations and call sites by walking SSA.
	ctx.collectObservations()

	// Phase 2: Infer guards from observations.
	ctx.inferGuards()

	// Phase 2.5: Derive per-function lock requirements from observations + guards.
	ctx.deriveInitialRequirements()

	// Phase 3: Propagate requirements and acquisitions through call graph.
	ctx.propagateRequirements()

	// Phase 3.5: Detect concurrent entrypoints and compute reachability.
	ctx.computeConcurrentContext()

	// Phase 4: Check violations (direct + interprocedural).
	ctx.checkViolations()
	ctx.checkInterproceduralViolations()

	return nil, nil
}
