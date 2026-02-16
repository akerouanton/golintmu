# C5 Design: Lock Leak / Missing Unlock

This document covers the design for **C5** (lock leak / missing unlock). C5 detects functions that acquire a lock but return on some code path without releasing it — leaving the mutex permanently locked and causing subsequent lock attempts to deadlock.

See also:
- [`docs/design.md`](design.md) — High-level architecture and core algorithm
- [`docs/catalog/C05-lock-leak.md`](catalog/C05-lock-leak.md) — C5 error catalog with examples
- [`docs/changelog.md`](changelog.md) — Iteration history

---

## Overview

Calling `Lock()` without a corresponding `Unlock()` on every return path leaves the mutex permanently locked. Every subsequent `Lock()` call deadlocks. The most common cause is early returns (error handling, validation) without using `defer`.

Three scenarios:

| Scenario | Example |
|----------|---------|
| Early return without defer | `if err { return err }` after `Lock()`, no `Unlock()` |
| Multiple early returns | Several return paths, only the last has `Unlock()` |
| Conditional defer | `defer Unlock()` in one branch only; the other branch leaks |

This check does not require guard inference — it operates purely on lock state tracking through control flow.

---

## Detection Point

Detection happens in `processInstruction` (`ssawalk.go`). Currently this function dispatches on `*ssa.Call`, `*ssa.Defer`, `*ssa.Store`, and `*ssa.UnOp`. C5 adds a `*ssa.Return` case.

```
func (ctx *passContext) processInstruction(fn, instr, ls):
    switch instr.(type):
    case *ssa.Call:     // existing
    case *ssa.Defer:    // existing (mode check) + NEW (record deferred unlock)
    case *ssa.Store:    // existing
    case *ssa.UnOp:     // existing
    case *ssa.Return:   // NEW: check for lock leaks
        ctx.checkReturnWithHeldLocks(fn, instr, ls)
```

At each `*ssa.Return`, inspect `ls.held` for locks not covered by a deferred unlock on the current path:

```
func (ctx *passContext) checkReturnWithHeldLocks(fn, ret, ls):
    for each ref, hl in ls.held:
        if ref in ls.deferredUnlocks:
            continue    // deferred unlock covers this lock
        ctx.lockLeakCandidates.add(fn, ret.Pos(), ref, hl.pos)
```

Candidates are collected during Phase 1 and reported in a new Phase 3.4 (after requirement propagation).

---

## Per-Path Deferred Unlock Tracking

### Why per-path?

The current code intentionally keeps locks in `ls.held` when `defer mu.Unlock()` is encountered — this is correct for guard inference (the lock is logically held through the function body). But it means at `*ssa.Return` every lock appears held, including those that will be released by deferred calls. C5 must distinguish "held and will be deferred-unlocked" from "held and leaked."

Deferred unlocks can be conditional:

```go
func (s *S) Example(cond bool) {
    s.mu.Lock()
    if cond {
        defer s.mu.Unlock()  // only on this branch
        return               // OK — defer covers it
    }
    return                   // BUG: mu leaked
}
```

If deferred unlocks were tracked at the function level (on `walkContext`), the defer on the `cond=true` branch would suppress the leak on the `cond=false` branch. Tracking on `lockState` (per-path) ensures the fork at the `if` produces two states: one with the deferred unlock, one without. Only the branch without the defer triggers C5.

### Data structure change

Add `deferredUnlocks` to `lockState`:

```go
type lockState struct {
    held            map[lockRef]heldLock
    deferredUnlocks map[lockRef]bool      // NEW: locks with pending deferred unlock on this path
}
```

### Method updates

**`fork()`** — copy `deferredUnlocks` alongside `held`.

**`intersect()`** — intersect `deferredUnlocks` (keep only common entries). If a deferred unlock is present on one branch but not the other, the merged state does NOT have it, so a return after the merge correctly flags the leak. This mirrors how `held` is intersected.

**`equalHeld()`** — extend to also compare `deferredUnlocks` for convergence detection.

### Recording deferred unlocks

Extend the `*ssa.Defer` case in `processInstruction`:

```
case *ssa.Defer:
    ctx.checkDeferredUnlockMismatch(fn, inst, ls)   // existing
    ctx.recordDeferredUnlock(fn, inst, ls)           // NEW
```

`recordDeferredUnlock` resolves the deferred call to a lockRef (same logic as `checkDeferredUnlockMismatch` — extract a shared helper to avoid duplication) and adds it to `ls.deferredUnlocks`.

---

## Lock Acquisition Position Tracking

To provide diagnostics like `return without unlocking X.mu (locked at file:line)`, we need to know where the lock was acquired. Add an acquisition position to `heldLock`:

```go
type heldLock struct {
    ref       lockRef
    exclusive bool
    pos       token.Pos   // NEW: position where the lock was acquired
}
```

Update the single call site in `checkAndRecordLockAcquire` to pass `pos` to `ls.lock()`.

---

## Suppression Heuristics

### Deferred unlock on the path

If `ls.deferredUnlocks[ref]` is true at the return point, the lock will be released by the deferred call. No diagnostic.

### Function `Requires` the lock

If a function's `funcLockFacts.Requires` contains the lock, the function expects the lock to be held at entry by convention. In practice this scenario cannot produce a C5 candidate (the walk starts with an empty `lockState`, so only locks acquired in the function appear in `ls.held`). The check is added as cheap insurance, consistent with the C4 suppression pattern.

### Constructor functions

Constructor functions (`New*`, `Make*`, `Create*`) are NOT excluded from C5. A lock leak in a constructor means the returned struct's mutex is permanently locked — the first caller to use it will deadlock. Constructor exclusion (used for C1 guard inference) does not apply here.

### Concurrent context filtering

Lock leaks cause deadlock regardless of concurrency context. If a function leaks a lock, the very next `Lock()` call on that mutex deadlocks — even in single-threaded code. C5 does NOT require concurrent context filtering. This matches C2 (double lock) and C4 (unlock of unlocked), which also fire regardless of concurrency.

### Annotation suppression

Standard `//mu:ignore` (on function) and `//mu:nolint` (on return line) suppression applies via `ctx.isSuppressed()`. Users can annotate intentional acquire helpers (functions that return with the lock held for the caller to release).

---

## Interaction with Other Diagnostics

### C5 vs C11 (inconsistent branch locking)

C11 fires at merge points when lock state diverges between branches. C5 fires at return points when locks are held without defer. Both can fire on the same code — they're independently useful:

- C11: "lock state is inconsistent across branches" (at merge point)
- C5: "this specific return leaks the lock" (at return instruction)

Both should be reported. No deduplication needed.

### C5 vs C4 (unlock of unlocked)

Independent diagnostics targeting different bugs. C5 fires on return-with-lock-held; C4 fires on unlock-without-lock-held. On the same function, one path may trigger C5 and a different path may trigger C4.

### C5 vs C13 (return while holding lock — future)

C5 is intra-functional: "you locked it, you should unlock it." C13 (future) is interprocedural: "you returned with it held — does the caller know?" For the initial C5, functions that intentionally return with a lock held should use `//mu:ignore`. C13 will handle the caller-callee contract analysis.

---

## Phase Placement

Following the C4 deferred-reporting pattern:

```
// Phase 3.3: Report unlock-of-unlocked (C4)
ctx.reportDeferredUnlockOfUnlocked()

// Phase 3.4: Report lock leaks (C5)        ← NEW
ctx.reportDeferredLockLeaks()

// Phase 3.5: Detect concurrent entrypoints
ctx.computeConcurrentContext()
```

`reportDeferredLockLeaks` iterates candidates and reports those not suppressed by `Requires` facts or `//mu:ignore` annotations.

---

## Diagnostic Output

```
db.go:19:3: return without unlocking DB.mu (locked at db.go:16:2)
```

Format: `<return-pos>: return without unlocking <StructName.fieldName> (locked at <acquire-pos>)`

---

## Files

- `lockstate.go` — Add `deferredUnlocks` to `lockState`; update `newLockState()`, `fork()`, `intersect()`, `equalHeld()`; add `pos` to `heldLock`; update `lock()` signature
- `ssawalk.go` — Add `*ssa.Return` case to `processInstruction`; add `recordDeferredUnlock`; extract shared defer-resolution helper from `checkDeferredUnlockMismatch`; update `lock()` call site to pass position
- `golintmu.go` — Add `lockLeakCandidate` struct; add `lockLeakCandidates` field to `passContext`; add Phase 3.4 call
- `reporter.go` — Add `reportDeferredLockLeaks` and `reportLockLeak` methods
- `golintmu_test.go` — Add `TestLockLeak` test function
- `testdata/src/lock_leak/lock_leak.go` (new) — Test cases: early return, multiple returns, defer safe, explicit unlock safe, conditional defer, loop body return, RWMutex leak, suppressed with `//mu:ignore`
