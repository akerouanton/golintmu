# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What This Is

golintmu is a Go static analysis linter that detects inconsistent mutex locking of struct fields. Unlike annotation-based tools (e.g. gVisor's checklocks), it **infers** which lock guards which field by observing access patterns — no `+checklocks:mu` annotations needed. Built on `golang.org/x/tools/go/analysis` and SSA.

## Commands

```bash
go test ./...                              # Run all tests
go test ./pkg/analyzer/                    # Run analyzer tests only
go test ./pkg/analyzer/ -run TestBasic     # Run a single test
go build ./cmd/golintmu                    # Build the CLI binary
golangci-lint run ./...                    # Run linters
```

Tests use `analysistest.Run` with `// want "regexp"` comments in testdata files. To add a test case, add Go code to `pkg/analyzer/testdata/src/<suite>/` with `// want` comments on lines expected to produce diagnostics.

## Architecture

The analyzer is a single `analysis.Analyzer` in `pkg/analyzer/` that runs in phases per package. The phase design and full algorithm are documented in `docs/design.md` §5. The entry point is `run()` in `golintmu.go`, which orchestrates the phases sequentially through `passContext`.

**Observation Collection** (`ssawalk.go`): Walks SSA CFG of every source function, tracking `lockState` (which mutexes are held). At each struct field read (`*ssa.UnOp` on `*ssa.FieldAddr`) or write (`*ssa.Store` to `*ssa.FieldAddr`), records an `observation` keyed by `fieldKey{StructType, FieldIndex}`. Each observation stores `SameBaseMutexFields`—the field indices of mutexes held on the **same struct instance** at that program point.

**Guard Inference** (`inference.go`): For each observed field, filters out constructor-like functions (`New*/Make*/Create*`, `init()`, functions returning the struct type), checks immutability (all non-constructor accesses are reads → skip), then infers the guard as the mutex field index most frequently held across observations.

**Violation Detection** (`reporter.go`): Re-walks observations for guarded fields. If the guard mutex's field index is absent from `SameBaseMutexFields`, reports a diagnostic.

### Key Design Decision: Normalized Lock References

SSA values are function-scoped (each method's receiver is a different `*ssa.Parameter`), so raw `lockRef` values can't compare across functions. Observations normalize this by storing `SameBaseMutexFields []int`—just the mutex field indices held on the same struct base—rather than full `lockRef` values. Guard inference and violation checking operate purely on these field indices, which are type-scoped and work across all methods.

## Naming Conventions

Never use error catalog IDs (C01, C11, etc.) in code — not in variable names, function names, method names, or comments. The catalog is a documentation artifact, not a code concept. Use descriptive names that convey meaning: `reportInconsistentLockState` not `reportC11`, `inconsistentLockReported` not `c11Reported`.

## Design Reference

- `docs/design.md` — Full design document (architecture, algorithm, iteration roadmap)
- `docs/catalog/C01-*.md` through `C14-*.md` — Error catalog with examples

After completing each iteration, update `docs/design.md` to mark it done: add a ✅ to the iteration heading, set its **Status: Completed**, note which error catalog IDs it now detects, and update the Status column in the §3 error catalog table.

## Code Review Workflow

After implementing features or fixes, run the `code-reviewer` and `code-security` subagents to check for issues before considering the task complete.

## Task Tracking

Use `bd` for task tracking.
