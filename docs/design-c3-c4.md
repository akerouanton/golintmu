# C3/C4 Design: Lock Ordering & Unlock of Unlocked

This document covers the design for two upcoming diagnostics: **C4** (unlock of unlocked mutex) and **C3** (lock ordering violations). C4 is simpler and can be implemented first; C3 builds on the same infrastructure.

See also:
- [`docs/design.md`](design.md) — High-level architecture and core algorithm
- [`docs/catalog/C03-lock-ordering.md`](catalog/C03-lock-ordering.md) — C3 error catalog with examples
- [`docs/catalog/C04-unlock-of-unlocked.md`](catalog/C04-unlock-of-unlocked.md) — C4 error catalog with examples
- [`docs/changelog.md`](changelog.md) — Iteration history

---

## C4: Unlock of Unlocked Mutex

### Overview

Calling `Unlock()` on a mutex that isn't held causes `panic: sync: unlock of unlocked mutex`. This covers three scenarios: unpaired unlock, double unlock, and branch-conditional unlock (unlock reachable on a path where the lock was never acquired).

### Detection Point

Detection happens in `checkAndRecordUnlock` (`ssawalk.go`). Currently this function only checks for mismatched unlock mode (e.g. `Unlock` after `RLock`). The C4 check adds an `else` branch: if the lock is **not in `ls.held` at all**, report.

```
func (ctx *passContext) checkAndRecordUnlock(fn, pos, ref, exclusiveUnlock, ls):
    if ref in ls.held:
        // existing: check for mismatched mode (C6)
    else:
        // NEW: lock not held → C4 diagnostic
        ctx.reportUnlockOfUnlocked(fn, pos, ref)
    ls.unlock(ref)
```

### Scenarios Detected

| Scenario | How it manifests |
|----------|-----------------|
| Unpaired unlock | `Unlock()` with no preceding `Lock()` in the function — lock never enters `ls.held` |
| Double unlock | Second `Unlock()` after first already removed it from `ls.held` |
| Branch-conditional unlock | Lock acquired in one branch only; `Unlock()` after merge reaches it on the unlocked path via COW fork + merge |

### Interaction with C11

C11 (inconsistent branch locking) fires when lock state diverges at a merge point. C4 fires when an unlock instruction executes with the lock not held. Both can fire on the same code — they're independently useful:

- C11 tells you "lock state is inconsistent across branches"
- C4 tells you "this specific `Unlock()` call will panic on some paths"

Both should be reported. No deduplication needed.

### Deferred Unlock Heuristic

A common Go pattern is:

```go
func (s *S) doWork() {
    s.mu.Lock()
    defer s.mu.Unlock()
    // ...
}
```

But consider a helper that only unlocks:

```go
func (s *S) unsafeUnlock() {
    s.mu.Unlock()  // called with lock held by callers
}
```

At the intra-function level, `unsafeUnlock` would trigger C4 since `s.mu` isn't in `ls.held` at the start of that function. This is an interprocedural concern — the caller holds the lock, not the callee.

**Heuristic for deferred unlocks:** If `defer mu.Unlock()` appears in a function that also calls `mu.Lock()` (or the lock is already held at function entry via interprocedural facts), suppress C4 on the deferred unlock. The pairing is evident.

For the standalone `unsafeUnlock()` case, C4 is correct at the intra-function level. Interprocedural suppression (knowing the caller holds the lock) is future work in C13 territory — functions that expect callers to hold locks. The existing `funcLockFacts.Requires` mechanism already captures this; C4 suppression can check: if the function has a `Requires` entry for this mutex, suppress the C4.

### Files

- `ssawalk.go` — Add `else` branch in `checkAndRecordUnlock`
- `reporter.go` — New `reportUnlockOfUnlocked` function
- `testdata/src/unlock_of_unlocked/` — Test cases: unpaired, double, branch-conditional, deferred (suppressed)

---

## C3: Lock Ordering Violations

### Overview

When multiple locks are acquired, inconsistent ordering across code paths creates deadlock potential. If goroutine A holds X and waits for Y while goroutine B holds Y and waits for X, both deadlock. C3 detects this by building a directed lock-order graph and finding cycles.

### Lock-Order Graph

**Nodes:** `mutexFieldKey{StructType, FieldIndex}` — same type-level granularity used by interprocedural analysis.

**Edges:** A directed edge A→B means "lock B was acquired while lock A was held." Edges are collected during the SSA walk whenever a lock acquisition occurs with other locks already held.

**Known limitation — same-type instances:** Because nodes are type-level (`mutexFieldKey`), two instances of the same struct type share the same node. The classic `Transfer(from, to *Account)` pattern (lock `from.mu` then `to.mu`) produces a self-edge A→A, which is a trivially detectable cycle. This is a known limitation: golintmu cannot distinguish `accountA.mu` from `accountB.mu` statically. The self-edge is still useful — it flags the pattern for manual review.

### Edge Collection (Phase 1)

Edges are collected in `checkAndRecordLockAcquire` (`ssawalk.go`). When lock B is acquired and `ls.held` contains locks A1, A2, ..., record edges A1→B, A2→B, etc.

```
func (ctx *passContext) checkAndRecordLockAcquire(fn, pos, ref, exclusive, ls):
    // existing: double-lock checks (C2, C6)
    // existing: record acquisition

    // NEW: record lock-order edges
    for each heldRef in ls.held:
        heldKey := normalize(heldRef)
        acquiredKey := normalize(ref)
        if heldKey != acquiredKey:
            ctx.lockOrderEdges.add(heldKey → acquiredKey, pos, fn)

    ls.lock(ref, exclusive)
```

Each edge records the position and function for diagnostic output.

### Interprocedural Edges (after Phase 3)

Phase 3 computes `AcquiresTransitive` for each function — all locks a function and its callees acquire. After Phase 3, for each call site where the caller holds locks, add edges from each caller-held lock to each lock the callee transitively acquires:

```
for each callSite in ctx.callSites:
    calleeAcquires := ctx.funcFacts[callSite.Callee].AcquiresTransitive
    for each heldByCallerKey in callSite.HeldByStructType:
        for each acquiredByCalleeKey in calleeAcquires:
            if heldByCallerKey != acquiredByCalleeKey:
                ctx.lockOrderEdges.add(heldByCallerKey → acquiredByCalleeKey, callSite.Pos, callSite.Caller)
```

This catches ordering violations that span function boundaries — e.g. function A locks X then calls B which locks Y, while function C locks Y then calls D which locks X.

### Cycle Detection (new Phase 3.7)

After interprocedural edge collection, run DFS-based cycle detection on the lock-order graph. A cycle indicates a potential deadlock.

**Algorithm:** Standard DFS with coloring (white/gray/black). When a back-edge is found (gray→gray), extract the cycle and report.

**Phase placement:** Between Phase 3 (interprocedural requirement propagation) and Phase 4 (violation detection). Call it Phase 3.7 — it needs `AcquiresTransitive` from Phase 3 but is independent of Phase 4.

### Concurrent Context Filtering

Lock ordering violations only matter when the involved locks can be held by different goroutines simultaneously. Apply the same concurrent context filter used by Phase 4: only report cycles where at least one edge originates from a function reachable from a concurrent entrypoint.

This avoids false positives in single-threaded setup code that happens to acquire locks in varying orders.

### Diagnostic Output

Report the cycle with the edges that form it:

```
db.go:24:2: potential deadlock: lock ordering cycle between DB.mu and TxLog.mu
  db.go:16:2: CommitWithLog() acquires DB.mu then TxLog.mu
  db.go:24:2: FlushToDB() acquires TxLog.mu then DB.mu
```

For cycles longer than 2, list all edges:

```
pipeline.go:11:2: potential deadlock: lock ordering cycle: Stage.mu → Stage.mu → Stage.mu
  pipeline.go:11:2: connect() acquires a.mu then b.mu
  (cycle involves 3 edges across 3 call sites)
```

### Data Structures

```go
// lockOrderEdge records that lockB was acquired while lockA was held.
type lockOrderEdge struct {
    From mutexFieldKey
    To   mutexFieldKey
    Pos  token.Pos       // where the second lock was acquired
    Fn   *ssa.Function   // function containing the acquisition
}
```

The graph is stored on `passContext` as a slice or adjacency map: `lockOrderEdges map[mutexFieldKey][]lockOrderEdge`.

### Files

- `lockorder.go` (new) — Lock-order graph data structure, edge storage, DFS cycle detection
- `ssawalk.go` — Edge collection in `checkAndRecordLockAcquire`
- `golintmu.go` — Add Phase 3.7 call between Phase 3 and Phase 4
- `interprocedural.go` — Interprocedural edge collection after Phase 3
- `reporter.go` — `reportLockOrderCycle` diagnostic formatting
- `testdata/src/lock_ordering/` — Test cases: two-lock inversion, multi-struct, three-lock cycle, same-type self-edge, interprocedural ordering

---

## Implementation Order

1. **C4 first** — Small, self-contained change. Adds an `else` branch to `checkAndRecordUnlock` and a new report function. No new data structures.
2. **C3 second** — Requires new `lockorder.go` file, edge collection hooks, Phase 3.7, and interprocedural edge propagation. Larger scope but builds on existing infrastructure.

Both can be implemented as single iterations following the existing pattern (scope, files, test cases, update design.md status).
