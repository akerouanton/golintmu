# golintmu Changelog

Iteration-by-iteration implementation history. For the high-level architecture, see [`docs/design.md`](design.md).

---

## Iteration 1: MVP core — Lock state tracking + inference + basic violations

**Status: Completed** — Detects C1 (inconsistent field locking).

**Files:** `go.mod`, `golintmu.go`, `lockstate.go`, `resolver.go`, `ssawalk.go`, `inference.go`, `reporter.go`, `cmd/golintmu/main.go`, `golintmu_test.go`, `testdata/src/basic/`

**Scope:**
- Single-package analysis only
- Track `sync.Mutex` field locks (Lock/Unlock) through SSA basic blocks
- No COW fork yet — simple map copy at branch points
- Collect field access observations with lock state
- Infer guards (any locked access triggers inference, minus constructors)
- Detect immutable fields (all writes in constructors) and skip them
- Report violations: field access without inferred lock held
- No interprocedural analysis yet — flag in the function where the access occurs
- No concurrent entrypoint filtering — flag everywhere
- No RWMutex distinction — treat RLock/RUnlock same as Lock/Unlock
- Basic test cases with `// want` comments

## Iteration 2: Defer handling + constructor heuristics

**Status: Completed**

**Files:** Update `ssawalk.go`, `inference.go`, add `testdata/src/defer_patterns/`, `testdata/src/false_positives/`

**Scope:**
- Handle `defer mu.Unlock()` correctly (lock stays held until return)
- Improve constructor detection: `New*`, `Make*`, `Create*`, return-type analysis
- Exclude `init()` from inference

## Iteration 3: COW fork + proper branch handling

**Status: Completed** — Detects C11 (inconsistent branch locking).

**Files:** Update `lockstate.go`, `ssawalk.go`, add `testdata/src/` branch test cases

**Scope:**
- Implement copy-on-write on `lockState` for efficient forking
- Fork at if/switch/select branches
- Handle loops with visited-block cache (block → lockState mapping, skip if compatible)
- Detect C11 (inconsistent lock state across branches)

## Iteration 4: Interprocedural analysis

**Status: Completed** — Detects C2 (double locking), interprocedural lock requirement propagation.

**Files:** `interprocedural.go`, update `golintmu.go`, `ssawalk.go`, `reporter.go`, add `testdata/src/interprocedural/`, `testdata/src/double_lock/`

**Scope:**
- Record call sites during SSA walk with normalized lock state
- Record lock acquisitions per function
- Bottom-up lock requirement inference (Phase 2.5: derive from observations + guards)
- Fixed-point propagation of requirements through intra-package call graph (Phase 3)
- Transitive acquisition propagation for double-lock detection
- Interprocedural violation detection at call sites (Phase 4)
- Suppression of direct violations when requirements propagate to call sites
- Intra-function double-lock detection (C2)
- Interprocedural double-lock detection (caller holds lock, callee acquires transitively)

## Iteration 5: Concurrent entrypoint detection

**Status: Completed** — Detects concurrent entrypoints (`go` targets, `ServeHTTP`, `http.HandleFunc`) and filters Phase 4 violations (C1, interprocedural missing-lock, interprocedural double-lock) to concurrent contexts only. Phase 1 diagnostics (C2 intra-function double-lock, C11 inconsistent branch locking) remain unfiltered.

**Files:** `concurrency.go`, update `golintmu.go`, `reporter.go`, add `testdata/src/concurrent/`

**Scope:**
- Detect `go` statement targets (functions and closures)
- Detect HTTP handler implementations (ServeHTTP, HandlerFunc)
- Detect functions passed to `http.HandleFunc`, `http.Handle`
- Compute reachability from concurrent entrypoints via BFS through call graph
- Only report Phase 4 violations in concurrent contexts (fallback: all concurrent if no entrypoints detected)

## Iteration 6: Annotations

**Status: Completed**

**Files:** `annotations.go`, update `golintmu.go`, `concurrency.go`, `reporter.go`, `ssawalk.go`, add `testdata/src/annotations/`

**Scope:**
- Parse `//mu:concurrent`, `//mu:ignore`, `//mu:nolint` comment directives
- `//mu:concurrent` marks functions as concurrent entrypoints (merged into detection)
- `//mu:ignore` suppresses all diagnostics in a function
- `//mu:nolint` suppresses the diagnostic on the next line only
- Suppression checks added to all five report functions

## Iteration 7: Cross-package facts

**Status: Completed** — Detects C14 (exported guarded field).

**Files:** `facts.go` (new), update `golintmu.go`, `inference.go`, `reporter.go`, add `testdata/src/crosspackage/`

**Scope:**
- Export `FieldGuardFact`, `FuncLockFact`, `ConcurrentFact` via `analysis.Fact` system
- Import facts when analyzing downstream packages (field guards, function requirements, concurrent markers)
- Warn when guarded field is exported (C14)
- Cross-package violation detection: field access and call-site checks using imported facts

## Iteration 8: Multi-mutex structs

**Status: Not started**

**Files:** Update `inference.go`, add `testdata/src/multimutex/`

**Scope:**
- When a struct has multiple mutexes, infer per-field which specific mutex guards each field
- Use co-occurrence: field F is guarded by the mutex most frequently held when F is accessed

## Iteration 9: RWMutex distinction

**Status: Completed** — Detects C6 (mismatched unlock, recursive RLock, lock upgrade attempt). Infers `NeedsExclusive` per guard.

**Files:** Updated `golintmu.go`, `lockstate.go`, `resolver.go`, `ssawalk.go`, `inference.go`, `reporter.go`, `interprocedural.go`, `facts.go`, added `testdata/src/rwmutex/`

**Scope:**
- Track exclusive (Lock) vs. read (RLock) lock level in `lockState`
- Infer whether a field only needs RLock for reads (`NeedsExclusive`)
- Detect mismatched unlock (Unlock after RLock, RUnlock after Lock — including deferred)
- Detect recursive RLock (deadlock risk with waiting writers)
- Detect lock upgrade attempt (Lock after RLock — deadlock)

## Iteration 10: Embedded mutexes

**Status: Completed** — Handles embedded `sync.Mutex` and `sync.RWMutex` as anonymous struct fields with promoted Lock/Unlock methods.

**Files:** Updated `resolver.go`, `ssawalk.go`, `golintmu_test.go`, added `testdata/src/embedded/`

**Scope:**
- Handle structs that embed `sync.Mutex` or `sync.RWMutex` directly (promoted Lock/Unlock/RLock/RUnlock)
- Resolve Lock/Unlock calls on the struct itself to the embedded mutex field via `resolveEmbeddedMutexRef` fallback
- Works with both inline field extraction (already handled) and wrapper calls (`(*S).Lock(s)`)
- Supports defer patterns, constructor exclusion, and mixed embedded + named mutex structs

## Iteration 11: C3 (lock ordering) and C4 (unlock of unlocked)

**Status: Completed** — Detects C4 (unlock of unlocked mutex — runtime panic) and C3 (lock ordering violations — potential deadlock).

**Files:** `lockorder.go` (new), updated `golintmu.go`, `ssawalk.go`, `interprocedural.go`, `reporter.go`, `double_lock/double_lock.go`, `golintmu_test.go`, added `testdata/src/unlock_of_unlocked/`, `testdata/src/lock_ordering/`

**C4 scope:**
- Extract `lockRefToMutexFieldKey` helper from `recordLockAcquisition` for reuse
- Add `unlockOfUnlockedCandidate` type and collection field on `passContext`
- Detect unlock-of-unlocked in `checkAndRecordUnlock` `else` branch (lock not in `ls.held`)
- Defer reporting to Phase 3.3 (after requirement propagation) for suppression accuracy
- Suppress C4 when function has `Requires` entry for the mutex (helper functions)
- Deferred unlocks (`Lock() + defer Unlock()`) naturally don't trigger C4
- Scenarios: unpaired unlock, double unlock, branch-conditional unlock

**C3 scope:**
- Lock-order graph data structure (`lockOrderGraph`) with directed edges between `mutexFieldKey` nodes
- Intra-function edge collection: when a lock is acquired while others are held, record held→acquired edges
- Skip same-instance self-edges (double-lock, already C2); allow same-type different-instance self-edges
- Interprocedural edge collection: caller-held locks → callee's `AcquiresTransitive` (skip same-key to avoid C2 overlap)
- DFS-based cycle detection with white/gray/black coloring
- Deterministic traversal via sorted nodes and edges
- Cycle deduplication (same cycle from different DFS start nodes)
- Concurrent context filtering: at least one edge in the cycle must originate from a concurrent function
- Phase 3.7 placement: after Phase 3 (requirement propagation) and Phase 3.5 (concurrent context), before Phase 4
- Scenarios: two-lock inversion, same-type self-edge, consistent ordering (no diagnostic), interprocedural ordering

## Iteration 13: Return while holding lock (C13) ✅

**Status: Completed** — Detects C13 (return while holding lock — acquire helpers and caller obligation).

**Files:** Updated `golintmu.go`, `ssawalk.go`, `interprocedural.go`, `reporter.go`, `facts.go`, `golintmu_test.go`; added `testdata/src/return_while_locked/`

**Scope:**
- Compute `ReturnsHolding` postcondition from C5 candidates: function returns with lock held on all paths
- Populate `Releases` set during Phase 1: track which functions explicitly call Unlock/defer Unlock
- Callee-side diagnostic: acquire helper functions annotated with "returns while holding — callers must unlock"
- Caller-side diagnostic: callers of acquire helpers that never release the acquired lock
- Suppress C5 diagnostics for acquire helpers (mutually exclusive with C13)
- Suppress C4 (unlock of unlocked) when caller unlocks a lock returned-holding by a callee
- Support `//mu:ignore` on callee (suppresses callee-side diagnostic only) and caller (suppresses caller-side)
- Cross-package export/import of `ReturnsHolding` in `FuncLockFact`
- Phase 3.8 placement: after C3 lock ordering, before C5 reporting
- C4 moved to Phase 3.9.3 (after C13) to benefit from `ReturnsHolding` for suppression
- Scenarios: acquire helper (lock and return), unaware caller, aware caller (explicit unlock), aware caller (defer unlock), chain of helpers (level-by-level), suppressed callee/caller/call-line, partial-path return (C5 not C13), RWMutex acquire helper

## Fix: Pre-publication constructor call suppression

**Status: Completed** — Eliminates false positive interprocedural diagnostics when constructors call setup methods on structs before publishing them to shared state.

**Files:** Updated `interprocedural.go`, `ssawalk.go`, `reporter.go`, `golintmu_test.go`, added `testdata/src/constructor_calls/`

**Problem:** Constructor exclusion (`isConstructorLike`) was applied to direct field observations but not to the interprocedural pipeline. When a constructor like `CreateConfig()` called `setup()` on a newly allocated struct before storing it in a shared map, the callee's lock requirements propagated to the call site — producing false "must be held when calling" diagnostics even though the struct was local and unreachable from other goroutines.

**Fix:**
- Track the receiver SSA value at each call site (`ReceiverValue` field on `callSiteRecord`)
- Add `isPrePublicationConstructorCall()` which checks: (1) callee is a method, (2) caller is constructor-like for the receiver's type, (3) the receiver hasn't been "published" (stored to a map or non-local field) before the call position
- Suppress requirement propagation and violation reporting for pre-publication constructor calls
- Publication detection uses position comparison within the same function, which is conservative for cross-branch cases

See design.md §6 "Pre-publication constructor call suppression" for full details.

---

## Future iterations (not scheduled)

Remaining items:
- Global mutex + global variable tracking
- `sync.Once` awareness (exclude `Do` callbacks from violation checking)
- `sync/atomic` awareness (skip fields accessed only through atomic functions)
- Lock wrappers / callback-under-lock detection
- Lock leak detection (C5) via return-point lock state checking
- C7 (deferred Lock instead of Unlock) detection
- C8 (lock held across goroutine spawn) detection
- C9 (lock held across blocking operations) detection
- gRPC / ConnectRPC handler detection
- Configurable framework handler patterns
- JSON / SARIF output
- golangci-lint plugin

---

## Verification Process

For each iteration:
1. **Unit tests**: `go test ./...` — runs `analysistest.Run` with testdata packages. Each test file uses `// want "regexp"` comments on lines expected to produce diagnostics.
2. **CLI smoke test**: `go run ./cmd/golintmu ./testdata/src/basic/` — verify human-readable output.
3. **Real-world validation**: After iteration 5+, run on a real codebase with known mutex patterns to measure false positive rate. Target: <5% false positive rate on well-written code.
4. **Regression**: Each iteration's test cases remain in the suite to prevent regressions.
