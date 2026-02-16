# C13 Design: Return While Holding Lock

This document covers the design for **C13** (return while holding lock). C13 detects functions that return with a lock held on all code paths (acquire helpers) and verifies that their callers correctly release the acquired lock.

See also:
- [`docs/design.md`](design.md) — High-level architecture and core algorithm
- [`docs/catalog/C13-return-while-locked.md`](catalog/C13-return-while-locked.md) — C13 error catalog with examples
- [`docs/changelog.md`](changelog.md) — Iteration history

---

## Overview

Some functions intentionally return with a lock held — "acquire helpers" that lock a mutex and return a value for the caller to use under protection. The caller is responsible for releasing the lock. When the caller forgets, the mutex stays locked and every subsequent `Lock()` call deadlocks.

This is the interprocedural counterpart to C5 (lock leak). C5 catches functions that leak a lock on *some* paths (early return without defer). C13 catches functions that return holding a lock on *all* paths — an intentional postcondition — and verifies their callers handle the obligation.

Two scenarios:

| Scenario | What fires | Example |
|----------|-----------|---------|
| Callee-side | Function returns with lock held on all paths | `lockAndGet()` acquires `r.mu` and returns without unlocking |
| Caller-side | Caller of an acquire helper never releases the lock | `Process()` calls `lockAndGet()` but never unlocks `r.mu` |

### Relationship to C5

C5 and C13 are mutually exclusive per function/lock pair:

- **C5**: lock held at return on **some** paths but not others → bug in the function itself (missing unlock on early return)
- **C13**: lock held at return on **all** paths → intentional postcondition; the bug (if any) is in the caller

When a function holds a lock at every return point for a given mutex, it is an acquire helper. C5 diagnostics for that function/lock pair are suppressed in favor of C13's caller-side check.

---

## Computing `ReturnsHolding`

`ReturnsHolding` is a new postcondition derived from C5's lock-leak candidates. During Phase 1, `checkReturnWithHeldLocks` already collects `lockLeakCandidate` entries for every return point where a lock is held without a deferred unlock. C13 lifts these per-return-point candidates to a per-function postcondition.

A function `f` has `ReturnsHolding(mfk)` if **all** return points of `f` hold lock `mfk` (i.e., every return point is a lock-leak candidate for that lock).

```
func computeReturnsHolding():
    // Group C5 candidates by (function, mutexFieldKey).
    for each (retPos, candidates) in ctx.lockLeakCandidates:
        for each candidate in candidates:
            mfk := lockRefToMutexFieldKey(candidate.Ref)
            group[candidate.Fn][mfk].returnPositions.add(retPos)

    // Count actual return instructions per function.
    for each fn in ctx.srcFuncs:
        returnCount[fn] = count of *ssa.Return instructions in fn's blocks

    // A function has ReturnsHolding(mfk) if every return is a candidate.
    for each (fn, mfkMap) in group:
        for each (mfk, info) in mfkMap:
            if len(info.returnPositions) == returnCount[fn]:
                ctx.funcFacts[fn].ReturnsHolding[mfk] = true
```

The `lockRefToMutexFieldKey` helper (already exists in `ssawalk.go`) normalizes function-scoped `lockRef` values to type-scoped `mutexFieldKey` for cross-function comparison.

---

## Data Structures

### `funcLockFacts` (internal, per-function)

Two new fields:

```go
type funcLockFacts struct {
    Requires           map[mutexFieldKey]bool // existing
    Acquires           map[mutexFieldKey]bool // existing
    AcquiresTransitive map[mutexFieldKey]bool // existing
    ReturnsHolding     map[mutexFieldKey]bool // NEW: locks held at all return points
    Releases           map[mutexFieldKey]bool // NEW: locks explicitly unlocked in this function
}
```

**`ReturnsHolding`** — Populated by `computeReturnsHolding()` in Phase 3.8. Records that a function is an acquire helper for the given mutex. Used for:
1. Suppressing C5 diagnostics for the same function/lock pair
2. Flagging the callee itself (informational: "callers must unlock")
3. Driving the caller-side check

**`Releases`** — Populated during Phase 1 in `checkAndRecordUnlock` and `recordDeferredUnlock`. Records that a function calls `Unlock()` (or `defer Unlock()`) for the given mutex. One map insert per unlock call — cheap. Provides the signal needed for the caller-side check without re-walking SSA.

### `FuncLockFact` (exported, cross-package)

Add `ReturnsHolding` to the serializable fact:

```go
type FuncLockFact struct {
    Requires           []MutexRef // existing
    Acquires           []MutexRef // existing
    AcquiresTransitive []MutexRef // existing
    ReturnsHolding     []MutexRef // NEW
}
```

`Releases` is **not** exported. It is only needed for the caller-side check, which operates on same-package call sites. Cross-package callers are checked when the downstream package is analyzed — at that point, the downstream analysis walks its own functions and populates `Releases` locally.

---

## Caller-Side Detection

Phase 3.8 runs `checkCallersOfAcquireHelpers` after `computeReturnsHolding`. For each call site where the callee has `ReturnsHolding(mfk)`, verify the caller handles the lock:

```
func checkCallersOfAcquireHelpers():
    for each callSite in ctx.callSites:
        calleeFacts := ctx.funcFacts[callSite.Callee]
        if calleeFacts == nil:
            continue

        for each mfk in calleeFacts.ReturnsHolding:
            callerFacts := ctx.funcFacts[callSite.Caller]

            // Caller handles the lock if it:
            // 1. Propagates the postcondition (itself returns holding it), OR
            // 2. Explicitly releases the lock (Unlock or defer Unlock)
            if callerFacts.ReturnsHolding[mfk]:
                continue  // propagation — caller is itself an acquire helper
            if callerFacts.Releases[mfk]:
                continue  // caller unlocks the mutex

            ctx.reportCallerMissingUnlock(callSite, mfk)
```

**Why `Releases` is sufficient:** If the caller ever calls `Unlock()` on the same mutex (directly or via defer), it demonstrates awareness of the lock obligation. We don't need to verify that the unlock happens on every path — that's C5's job. C13 only checks whether the caller has *any* unlock for the acquired lock.

---

## Suppression Heuristics

### `//mu:ignore` on callee

Suppresses the callee-side diagnostic ("returns while holding lock — callers must unlock"). Does **not** suppress the caller-side diagnostic — if a caller fails to unlock, that's still a bug regardless of the callee's annotation.

### `//mu:ignore` on caller

Suppresses the caller-side diagnostic for that specific caller.

### `//mu:nolint` on call line

Suppresses the caller-side diagnostic for that specific call site.

### No constructor exclusion

Constructor functions (`New*`, `Make*`, `Create*`) are **not** excluded from C13. A constructor that returns with a lock held is a valid acquire helper pattern. Its callers must still unlock.

### No concurrent context filtering

Like C5, lock leaks from acquire helpers cause deadlock regardless of concurrency context. The next `Lock()` call on the leaked mutex deadlocks even in single-threaded code. C13 does **not** require concurrent context filtering.

---

## Interaction with Other Diagnostics

### C5 (lock leak / missing unlock)

**Mutually exclusive per function/lock pair.** `reportDeferredLockLeaks` skips candidates where the function has `ReturnsHolding` for the same lock. Rationale: if a function holds a lock at all return points, that's a postcondition, not a bug. The C13 caller-side check handles the obligation transfer.

```
func reportDeferredLockLeaks():
    for each candidates in ctx.lockLeakCandidates:
        for each candidate in candidates:
            if ctx.functionRequiresMutex(candidate.Fn, &candidate.Ref):
                continue  // existing suppression
            mfk := lockRefToMutexFieldKey(candidate.Ref)
            if ctx.funcFacts[candidate.Fn].ReturnsHolding[mfk]:
                continue  // NEW: suppress C5 when C13 applies
            ctx.reportLockLeak(candidate)
```

### C2 (double lock)

No interaction. A caller that acquires the lock itself and then calls an acquire helper gets a C2 diagnostic (double lock) from existing logic — the call-site interprocedural check in Phase 4 already handles this via `AcquiresTransitive`.

### C4 (unlock of unlocked)

No interaction. C4 operates on explicit unlock calls where the lock isn't held. C13 operates on call sites where a postcondition creates an unlock obligation.

---

## Phase Placement

Phase 3.8, after Phase 3.7 (C3 lock ordering). C13 also requires moving C5 reporting from Phase 3.4 to Phase 3.9 so that `ReturnsHolding` is available for suppression (since `analysis.Pass` diagnostics are fire-and-forget, C5 must not be reported before `computeReturnsHolding` runs).

The resulting phase order:

```
// Phase 3.3: Report unlock-of-unlocked (C4).
ctx.reportDeferredUnlockOfUnlocked()

// Phase 3.5: Detect concurrent entrypoints and compute reachability.
ctx.computeConcurrentContext()

// Phase 3.7: Collect interprocedural lock-order edges and detect cycles.
ctx.collectInterproceduralLockOrderEdges()
ctx.detectAndReportLockOrderCycles()

// Phase 3.8: Detect acquire helpers and check their callers (C13).
ctx.computeReturnsHolding()
ctx.checkCallersOfAcquireHelpers()

// Phase 3.9: Report lock leaks (C5), suppressing acquire helpers.
ctx.reportDeferredLockLeaks()  // moved from Phase 3.4

// Phase 4: Check violations (direct + interprocedural).
ctx.checkViolations()
ctx.checkInterproceduralViolations()
```

Phase 3.8 needs:
- C5 candidates from Phase 1 (for `computeReturnsHolding`)
- `funcLockFacts` from Phase 3 (for `Releases` and requirement propagation)
- Imported `FuncLockFact.ReturnsHolding` from Phase 1.5 (cross-package callees)

Moving `reportDeferredLockLeaks` to Phase 3.9 is safe: it only reads `lockLeakCandidates` and `funcFacts` — no downstream phase depends on its output.

---

## Diagnostic Output

### Callee-side

```
registry.go:11:1: lockAndGet() returns while holding Registry.mu -- callers must unlock
```

Format: `<func-pos>: <funcName>() returns while holding <StructName>.<fieldName> -- callers must unlock`

Reported once per acquire helper, at the function's position. Suppressed by `//mu:ignore` on the function.

### Caller-side

```
registry.go:16:9: Process() calls lockAndGet() which acquires Registry.mu, but Process() never releases it
```

Format: `<call-pos>: <callerName>() calls <calleeName>() which acquires <StructName>.<fieldName>, but <callerName>() never releases it`

Reported at the call site position. Suppressed by `//mu:ignore` on the caller function or `//mu:nolint` on the call line.

---

## Cross-Package Support

### Export (Phase 5)

`exportFuncLockFacts` adds `ReturnsHolding` to the serialized `FuncLockFact`:

```go
ctx.pass.ExportObjectFact(fn.Object(), &FuncLockFact{
    Requires:           mutexFieldKeySetToRefs(facts.Requires),
    Acquires:           mutexFieldKeySetToRefs(facts.Acquires),
    AcquiresTransitive: mutexFieldKeySetToRefs(facts.AcquiresTransitive),
    ReturnsHolding:     mutexFieldKeySetToRefs(facts.ReturnsHolding),  // NEW
})
```

The export condition extends: export if any of `Requires`, `Acquires`, `AcquiresTransitive`, or `ReturnsHolding` is non-empty.

### Import (Phase 1.5)

`importFuncLockFacts` deserializes `ReturnsHolding` into the callee's `funcLockFacts`:

```go
for _, ref := range fact.ReturnsHolding {
    if mfk, ok := ctx.mutexRefToKey(ref); ok {
        facts.ReturnsHolding[mfk] = true
    }
}
```

`Releases` is **not** exported or imported — it is populated locally during Phase 1 for each package's own functions.

---

## Populating `Releases`

### In `checkAndRecordUnlock`

When a regular (non-deferred) unlock call is processed, record the release:

```
func (ctx *passContext) checkAndRecordUnlock(fn, pos, ref, exclusiveUnlock, ls):
    if ref in ls.held:
        // existing: mismatched mode checks
    else:
        // existing: C4 candidate collection
    ls.unlock(ref)

    // NEW: record that this function releases this mutex.
    mfk, ok := lockRefToMutexFieldKey(ref)
    if ok:
        ctx.getOrCreateFuncFacts(fn).Releases[mfk] = true
```

### In `recordDeferredUnlock`

When a deferred unlock is recorded, also mark the release:

```
func (ctx *passContext) recordDeferredUnlock(d, ls):
    ref, methodName := resolveDeferredLockRef(d)
    if ref == nil || isLockAcquire(methodName):
        return
    ls.deferUnlock(*ref)

    // NEW: record that this function releases this mutex.
    mfk, ok := lockRefToMutexFieldKey(ref)
    if ok:
        fn := d.Parent()
        ctx.getOrCreateFuncFacts(fn).Releases[mfk] = true
```

One map insert per unlock call. No additional SSA traversal.

---

## Design Decisions

### No transitive `ReturnsHolding` propagation

In the initial implementation, `ReturnsHolding` is not transitively propagated through the call graph. If function A calls B which calls C, and C has `ReturnsHolding(mu)`, then B gets `ReturnsHolding(mu)` only if B itself holds `mu` at all its return points. A does not automatically inherit C's postcondition.

This is sufficient because the caller-side check catches the same bugs level by level: if B doesn't release C's lock, B is flagged. If B propagates (itself returns holding), then A is checked.

### `Releases` as a simple set

`Releases` is a `map[mutexFieldKey]bool` — it only records *whether* a function ever unlocks a given mutex, not *where* or *how many times*. This is intentional:
- We only need to know if the caller has demonstrated awareness of the lock obligation
- Path-sensitive analysis of whether the unlock happens on all paths is C5's concern, not C13's
- A simple set is cheap (one map insert per unlock) and avoids complexity

---

## Files

- **`interprocedural.go`** — Add `ReturnsHolding` and `Releases` fields to `funcLockFacts`; initialize in `newFuncLockFacts`
- **`ssawalk.go`** — Populate `Releases` in `checkAndRecordUnlock` and `recordDeferredUnlock`
- **`golintmu.go`** — Add Phase 3.8 calls (`computeReturnsHolding`, `checkCallersOfAcquireHelpers`); move `reportDeferredLockLeaks` to Phase 3.9
- **`reporter.go`** — Add `computeReturnsHolding`, `checkCallersOfAcquireHelpers`, `reportAcquireHelper`, `reportCallerMissingUnlock`; update `reportDeferredLockLeaks` to suppress C5 when `ReturnsHolding` applies
- **`facts.go`** — Add `ReturnsHolding` to `FuncLockFact`; update import/export logic
- **`golintmu_test.go`** — Add `TestReturnWhileLocked` test function
- **`testdata/src/return_while_locked/` (new)** — Test cases: acquire helper (callee-side diagnostic), unaware caller (caller-side diagnostic), correct caller (unlock after call), correct caller (defer unlock), propagating acquire helper (chain), suppressed callee (`//mu:ignore`), suppressed caller (`//mu:ignore`), suppressed call line (`//mu:nolint`), partial-path return (C5 not C13), RWMutex acquire helper
