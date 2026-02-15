# golintmu Design Document

## 1. Problem Statement

Go's `sync.Mutex` and `sync.RWMutex` protect shared state from data races, but the language provides no static enforcement that:

- Fields are consistently accessed under the correct lock
- Locks are acquired in a consistent order across code paths
- Lock/unlock calls are correctly paired
- Lock discipline is maintained across function call boundaries

The built-in race detector (`go test -race`) catches races at runtime but only on exercised code paths. Google's gVisor [checklocks](https://github.com/google/gvisor/tree/master/tools/checklocks) enforces lock discipline through explicit annotations on struct fields (`+checklocks:mu`). This requires developers to manually annotate every guarded field.

### Why not checklocks?

Annotation-based tools like checklocks only protect fields that developers remember to annotate. In a codebase that evolves organically, a field that was previously single-threaded may become shared as new features are added. If the developer introducing the concurrent access path does not also add the `+checklocks:mu` annotation — and if they were unaware of the concurrency requirement, they likely won't — the tool remains silent.

**golintmu** takes a different approach with two complementary mechanisms:

- **Guard inference**: if a field is accessed under a lock anywhere in the codebase, all unprotected accesses are flagged automatically. The protection adapts as the code evolves without manual upkeep.
- **Concurrent entrypoint analysis**: when a field of a mutex-bearing struct is accessed from a concurrent context (goroutine, HTTP handler, etc.) without any lock held, golintmu flags it — even if no other locked access exists yet. The presence of a mutex on the struct combined with concurrent access is sufficient signal.

Together, these ensure that code which becomes concurrent is automatically detected and flagged when lock protection is missing.

## 2. Goals and Non-Goals

### Goals

- Detect inconsistent locking: fields accessed under a lock in some code paths but not others
- Interprocedural analysis: track lock state across function calls, flag callers that don't hold required locks
- Infer which lock guards which field without explicit annotations
- Detect double-locking (immediate deadlock with Go's non-recursive mutexes)
- Minimize false positives through constructor exclusion, immutability detection, and configurable thresholds
- Extensible design that supports additional checks (lock ordering, lock leaks, RWMutex misuse) in future iterations
- Compatible with `go vet -vettool` and the `golang.org/x/tools/go/analysis` framework
- golangci-lint plugin (post-MVP)

### Non-Goals (for now)

- Annotations on struct fields or variables (the core differentiator vs. checklocks)
- Runtime analysis or instrumentation
- Channel-based or actor-model synchronization analysis
- Full pointer alias analysis

## 3. Comprehensive Catalog of Detectable Errors

golintmu's core design (SSA-based lock state tracking + interprocedural propagation) supports detecting the following classes of mutex bugs. Each check has a detailed page with examples in [`docs/catalog/`](catalog/).

| ID | Name | Severity | Phase | Interprocedural | Details | Status |
|----|------|----------|-------|-----------------|---------|--------|
| [C1](catalog/C01-inconsistent-field-locking.md) | Inconsistent field locking | Error | Iteration 1 | Yes | Field accessed under lock in some paths, without in others | **Done** |
| [C2](catalog/C02-double-locking.md) | Double locking | Error | Iteration 4 | Yes | Mutex locked when already held — immediate deadlock | **Done** |
| [C3](catalog/C03-lock-ordering.md) | Lock ordering violations | Error | Future | Yes | Inconsistent acquisition order across code paths — potential deadlock | |
| [C4](catalog/C04-unlock-of-unlocked.md) | Unlock of unlocked mutex | Error | Future | No | `Unlock()` when mutex isn't held — runtime panic | |
| [C5](catalog/C05-lock-leak.md) | Lock leak / missing unlock | Error | Future | No | Function returns without unlocking on some code path | |
| [C6](catalog/C06-rwmutex-misuse.md) | RWMutex misuse | Error | Iter 9 | Yes | Mismatched unlock, recursive RLock, lock upgrade attempt | **Done** |
| [C7](catalog/C07-deferred-lock.md) | Deferred Lock instead of Unlock | Error | Future | No | `defer mu.Lock()` typo — deadlock at function exit | |
| [C8](catalog/C08-lock-across-goroutine.md) | Lock held across goroutine spawn | Warning | Future | No | Goroutine spawned while lock is held | |
| [C9](catalog/C09-lock-across-blocking.md) | Lock held across blocking ops | Warning | Future | No | Channel/sleep/I/O while lock is held | |
| [C10](catalog/C10-mutex-copying.md) | Mutex copying | Error | Future | No | Mutex copied by value — breaks synchronization | |
| [C11](catalog/C11-inconsistent-branch-locking.md) | Inconsistent branch locking | Error | Iteration 3 | No | Lock held in one branch but not the other at merge point | **Done** |
| [C12](catalog/C12-cross-goroutine-unlock.md) | Cross-goroutine unlock | Warning | Future | Yes | Lock/unlock in different goroutines — fragile pattern | |
| [C13](catalog/C13-return-while-locked.md) | Return while holding lock | Warning | Future | Yes | Function returns with lock held, caller unaware | |
| [C14](catalog/C14-exported-guarded-field.md) | Exported guarded field | Warning | Iteration 7 | Cross-pkg | Guarded field is exported — external callers can bypass lock | **Done** |

> **Implementation scope:** Early iterations focus on **C1** and **C2**. The core design naturally supports C4, C5, C7, C8, C11, and C13 — they all fall out of checking `lockState` at the right program points. C3 adds a lock-order graph. C6 extends `lockState` to track lock level. C9, C10, and C12 are specialized analyses built on the same infrastructure.

## 4. Synchronization Primitives Catalog

golintmu's MVP tracks `sync.Mutex` and `sync.RWMutex` only. This section documents all Go synchronization primitives and how they relate to the linter's design.

### Tracked (MVP)

| Primitive | Methods | Notes |
|-----------|---------|-------|
| `sync.Mutex` | `Lock()`, `Unlock()` | Non-recursive. Double-lock = deadlock. |
| `sync.RWMutex` | `Lock()`, `Unlock()`, `RLock()`, `RUnlock()` | Read level tracked in future iteration. MVP treats RLock as Lock. |

### Recognized but not tracked (future)

| Primitive | Relevance | Design Impact |
|-----------|-----------|---------------|
| `sync.Once` | `Do()` callback is synchronized; field accesses inside should not trigger guard inference. | Detect `Once.Do(func(){...})` and exclude the closure from violation checking. Common source of false positives. |
| `sync.Map` | Already thread-safe. Fields of type `sync.Map` should not be flagged. | Type-check: if field type is `sync.Map`, skip guard inference for it. |
| `sync.WaitGroup` | `Add`/`Done`/`Wait` provide barrier synchronization. Not a lock. | No direct interaction with lock analysis. Could be used to detect concurrent boundaries. |
| `sync.Cond` | `Wait()`/`Signal()`/`Broadcast()` — wraps a `sync.Locker`. | `Cond.L` is the underlying lock. `Wait()` releases and re-acquires it. Complex to model. |
| `sync.Pool` | Thread-safe pool. | No impact — Pool fields are not guarded. |
| `sync/atomic` | Atomic operations provide lock-free synchronization. | Fields accessed only through `atomic.*` functions are self-synchronized and should not be flagged. |
| Channels | Ownership transfer and signaling. | Extremely hard to analyze statically. Out of scope. |
| `context.Context` | Carries cancellation/deadline, not synchronization. | No impact. |

## 5. Architecture

### Analysis Framework

- Built on `golang.org/x/tools/go/analysis` (compatible with `go vet -vettool`)
- Single `Analyzer` that depends on `buildssa.Analyzer` for SSA form
- Uses `analysis.Fact` system for cross-package analysis
- CLI via `singlechecker.Main`

**Trade-off: Single vs. multiple analyzers.** A single analyzer is simpler and is the pattern used by gVisor's checklocks. Multiple composed analyzers (e.g., one for lock state collection, one for inference, one for violations) would be more modular but add complexity — the phases are tightly coupled through the SSA walk and passContext state, making separation artificial. We use a single analyzer with internal phases. This can be revisited if the analyzer grows unwieldy.

### Core Algorithm

The analysis runs in 4 phases per package:

```
Phase 1: Observation Collection (SSA walk)
  For every function in the package:
    Walk SSA CFG, tracking which locks are held at each point (lockState)
    At every struct field access, record an "observation":
      (structType, fieldIndex, heldLocks, isRead, function, position)
    At every function call, record the caller's lock state at the call site

Phase 2: Guard Inference
  For each struct that contains a sync.Mutex or sync.RWMutex:
    For each non-mutex field:
      Exclude observations from constructors/init
      If all writes are in constructors → field is immutable → skip
      If ANY remaining observation shows the field accessed under a lock:
        Infer which lock guards this field (by co-occurrence)

Phase 3: Function Lock Requirement Inference (bottom-up, interprocedural)
  For each function F:
    For each guarded field accessed by F without F itself holding the lock:
      F requires that lock to be held by its callers
  Propagate transitively up the call graph until fixed point

Phase 4: Violation Detection (top-down, interprocedural)
  For each call site in a concurrent context:
    If callee requires lock X and caller doesn't hold X:
      Report diagnostic with call chain
  For each field access where the field is guarded:
    If the guard lock is not held (and function doesn't propagate requirement):
      Report diagnostic
```

### Lock State Tracking

Modeled after gVisor's `state.go`:

- `lockState` struct tracks which locks are currently held as a set of `lockRef`
- **Copy-on-write fork** at CFG branch points for efficient state splitting
- Detects `Lock()` / `Unlock()` / `RLock()` / `RUnlock()` calls on `sync.Mutex` and `sync.RWMutex`
- Handles `defer mu.Unlock()` by keeping the lock held through function exit
- Propagates lock state through basic blocks sequentially
- At CFG merge points, incompatible lock states (held in one branch, not the other) can be flagged (C11)
- Visited-block cache prevents re-walking blocks with the same lock state (handles loops)

### Lock Reference Model

A `lockRef` identifies a specific lock instance:

```
lockRef = {kind, fieldPath}    (within a struct)
lockRef = {kind, global}       (package-level variable — future)
lockRef = {kind, paramIndex, fieldPath}  (function parameter — future)
```

**MVP scope:** Only `fieldLock` (a mutex field of a struct).

Resolution traces SSA values back to their origin: `*ssa.FieldAddr` → struct field path, `*ssa.Parameter` → parameter index, `*ssa.Global` → global, `*ssa.Phi` → merge if all edges agree, `*ssa.Alloc` → local struct.

**Lock reference equality** is critical. Two `lockRef` values must be equal when they refer to the same logical lock, even through different SSA values. Within a function, this is straightforward (same base SSA value + field path). Across functions, lock references are normalized to (type, field index path).

### Guard Inference Algorithm

The inference maps `(structType, fieldIndex) → guardInfo`:

1. **Identify candidate structs**: Any named struct type containing a `sync.Mutex` or `sync.RWMutex` field (directly or embedded).

2. **Collect observations**: For each field of a candidate struct, gather all access observations from Phase 1.

3. **Filter observations**:
   - Exclude constructor-like functions: `New*`, `Make*`, `Create*`, functions returning the struct type
   - Exclude `init()` functions
   - Exclude test files (by default; configurable)

4. **Check immutability**: If all remaining writes are in excluded functions (constructors/init) and all other observations are reads, the field is immutable after construction → do not infer a guard.

5. **Infer guard (per-field)**: Among remaining observations, if ANY observation shows the field accessed while a lock is held:
   - Determine which lock: group observations by which mutex was held
   - If a struct has multiple mutexes (mu1, mu2), pick the mutex that is held in the most observations of this field
   - Record: "field F is guarded by lock L"

6. **Self-exclusion**: A mutex field is never inferred as guarded by itself.

### Interprocedural Analysis

This is a key differentiator. Lock state is tracked across function call boundaries.

**Bottom-up requirement inference:**
- If function F accesses a guarded field without holding the lock, F has a "lock requirement"
- If F calls G, and G has a lock requirement that F doesn't satisfy (F doesn't hold the lock), then F inherits that requirement
- Propagation continues until fixed point (handles recursive calls via iteration)

**Top-down violation detection:**
- Starting from concurrent entrypoints, check that every call site satisfies the callee's lock requirements
- If a callee requires lock X and the caller doesn't hold X → violation

**Interface calls are treated as opaque.** When a method is called through an interface, golintmu does not attempt to resolve the concrete type. This is by design: lock discipline should not leak across interface boundaries. If a function acquires a lock and then calls an interface method, the lock state does not propagate into the callee. This is conservative (may miss some bugs) but avoids false positives from imprecise call graph resolution. Locks leaking across interfaces is a design smell and should be refactored.

**Example:**

```go
func (s *S) Serve(w http.ResponseWriter, r *http.Request) {
    s.mu.Lock()
    s.helper()      // caller holds s.mu: OK
    s.mu.Unlock()
}

func (s *S) BadCaller() {
    s.helper()      // caller does NOT hold s.mu: VIOLATION
}

func (s *S) helper() {
    s.count++       // accesses guarded field, doesn't lock itself
}
```

**Inference**: `helper()` accesses `s.count` (guarded by `s.mu`) without locking → `helper()` requires `s.mu` held by caller.

**Detection**: `BadCaller()` calls `helper()` without holding `s.mu` → report violation.

**Diagnostic output** shows the call chain:
```
server.go:15:5: s.mu must be held when calling s.helper()
  server.go:15:5: BadCaller() calls helper() without holding s.mu
  server.go:20:5: helper() accesses s.count (guarded by s.mu)
```

For deeper chains, intermediate frames are elided:
```
server.go:15:5: s.mu must be held when calling s.handler()
  → eventually accesses s.count at server.go:42:5 (guarded by s.mu)
  (3 intermediate calls elided, use -trace-depth=N for full trace)
```

**Cycle handling**: Recursive call chains are handled by iterating until a fixed point. If function A calls B and B calls A, we process them until no new requirements are discovered.

### Concurrent Entrypoint Detection

Violations only matter in code that can run concurrently. Detection strategies:

| Strategy | How | Phase |
|----------|-----|-------|
| `go` statement targets | Detect `*ssa.Go` instructions | MVP |
| HTTP handlers | `ServeHTTP` method, `http.HandlerFunc` type, args to `http.HandleFunc`/`http.Handle` | MVP |
| Explicit annotation | `//mu:concurrent` on function | MVP |
| Reachability | Transitive closure from known entrypoints via call graph | MVP |
| gRPC service methods | Implementations of generated service interfaces | Future |
| ConnectRPC handlers | Similar pattern detection | Future |
| Exported methods heuristic | Any exported method on a struct with a mutex | Future (opt-in) |

**Reachability:** Any function reachable from a concurrent entrypoint (via direct calls) is also in a concurrent context. Interface dispatch does NOT propagate concurrency context (consistent with the opaque treatment above).

### Annotations

Minimal annotation support, on functions only:

```go
//mu:concurrent   — marks function as a concurrent entrypoint
//mu:ignore       — suppresses all diagnostics in this function
//mu:nolint       — suppresses diagnostic on next line
```

No annotations on struct fields or variables.

### Cross-Package Analysis

Three fact types exported via `analysis.Fact`:

- `FieldGuardFact` — per struct field: which lock (field index path) guards it, confidence level
- `FuncLockFact` — per function: lock requirements (must-hold locks) and postconditions (acquires/releases)
- `ConcurrentFact` — per function: marks as concurrent entrypoint

Facts are gob-encoded and persisted by the analysis framework. When analyzing package B that imports types from A, golintmu imports A's facts to check B's code against A's inferred guards.

### Diagnostic Output

Human-readable diagnostics via `pass.Reportf()`, compatible with standard Go tooling.

**Call chain display:** When a violation involves an interprocedural call chain, show up to 3 frames by default:

```
server.go:15:5: s.mu must be held when calling s.helper()
  server.go:15:5: BadCaller() calls helper() without holding s.mu
  server.go:20:5: helper() accesses s.count (guarded by s.mu)
```

For deeper chains, intermediate frames are elided:
```
server.go:15:5: s.mu must be held when calling s.handler()
  → eventually accesses s.count at server.go:42:5 (guarded by s.mu)
  (3 intermediate calls elided, use -trace-depth=N for full trace)
```

**Alternative considered:** A `-v` flag for verbose output (show all frames always) vs. `-trace-depth=N` for fine-grained control. Starting with 3 frames max; may switch to configurable depth if users need it.

**Future output formats:** JSON (`-json` flag) and SARIF for CI integration (GitHub Code Scanning, etc.). Not in MVP.

## 6. False Positive Mitigation

False positives are the primary risk for adoption. The following strategies mitigate them:

### Constructor exclusion
Functions named `New*`, `Make*`, `Create*`, or that return the struct type are considered constructors. Field accesses inside them don't count toward inference and aren't flagged. The struct isn't shared yet.

### `init()` exclusion
Package `init()` functions run single-threaded before `main()`. Excluded from both inference and violation checking.

### Immutability detection
If a field is written only in constructors/init and all subsequent accesses are reads, it's effectively immutable and doesn't need a lock. Detected by tracking write sites across all functions.

### Concurrent context requirement
Violations are only reported in code reachable from a concurrent entrypoint. Single-threaded code paths (main setup, init) are not flagged.

### Test file exclusion
Test files (`_test.go`) are skipped by default. Test code often sets up state in a single goroutine without locks. Configurable via `-test` flag.

### Explicit suppression
`//mu:nolint` suppresses the diagnostic on the next line. `//mu:ignore` suppresses all diagnostics in a function. Use sparingly.

### Known problematic patterns
- **Read-only access in `String()` methods**: Common to read fields without lock for debugging. May need special handling or be suppressed.
- **Lazy initialization with `sync.Once`**: Fields set in a `Once.Do` callback are synchronized but golintmu doesn't understand `Once` yet. May produce false positives until `sync.Once` awareness is added.
- **Atomic field access**: Fields accessed only through `sync/atomic` functions are self-synchronized. May produce false positives until atomic awareness is added.
- **Lock wrappers**: Functions like `func (s *S) withLock(fn func()) { s.mu.Lock(); fn(); s.mu.Unlock() }` — the callback runs with the lock held, but golintmu treats it as opaque. Document as known limitation.

### Per-field inference precision
When a struct has multiple mutexes, inference determines which specific mutex guards each field (by co-occurrence), not just "any mutex." This avoids false positives where different fields are guarded by different locks.

## 7. Dependencies

The only allowed dependency is `golang.org/x/tools`. No other external modules should be added — the analyzer must remain lightweight with a minimal dependency footprint.

## 8. Implementation Status

Currently detected catalog IDs:

| ID | Name | Since |
|----|------|-------|
| C1 | Inconsistent field locking | Iteration 1 |
| C2 | Double locking | Iteration 4 |
| C6 | RWMutex misuse | Iteration 9 |
| C11 | Inconsistent branch locking | Iteration 3 |
| C14 | Exported guarded field | Iteration 7 |

For the full iteration-by-iteration history, see [`docs/changelog.md`](changelog.md).

For upcoming C3 (lock ordering) and C4 (unlock of unlocked) designs, see [`docs/design-c3-c4.md`](design-c3-c4.md).

## 9. Key Design Decisions Summary

| Decision | Choice | Rationale |
|----------|--------|-----------|
| Single vs. multiple analyzers | Single with internal phases | Phases are tightly coupled; proven pattern (gVisor) |
| Inference threshold | Any locked access | Maximizes bug detection; false positives mitigated by constructor/immutability exclusion |
| Interprocedural analysis | From early iterations | Core requirement: real apps acquire locks and access data in different parts of the call graph |
| Interface dispatch | Opaque (don't resolve) | Conservative; locks leaking across interfaces is a design smell |
| Immutable field detection | Write-site analysis | If all writes are in constructors, field is safe without lock |
| Concurrent context | Required for violations | Avoids flagging single-threaded setup code |
| Test files | Skip by default | Test code often accesses fields without locks for setup |
| Annotations on data | None | Core differentiator — inference only |
| Annotation prefix | `//mu:` | Concise; consistent with tool purpose |
| Lock wrappers | Not handled (MVP) | Treat callbacks as opaque; document limitation |
| Diagnostic depth | 3 frames max (default) | Readable; `-trace-depth=N` for tunability |
| Output format | Human-readable (MVP) | JSON/SARIF are follow-ups |
| Multi-mutex inference | Per-field by co-occurrence | Correctness over simplicity |
| Exported guarded fields | Warn | Encourage encapsulation; cross-package facts are future |
