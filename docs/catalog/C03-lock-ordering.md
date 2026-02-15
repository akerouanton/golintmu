# C3: Lock Ordering Violations

| | |
|---|---|
| **Severity** | Error |
| **Phase** | Future |
| **Requires** | Lock state tracking, lock-order graph, cycle detection |
| **Interprocedural** | Yes |

## Description

When multiple locks are involved, acquiring them in different orders across code paths creates the potential for deadlocks. If goroutine A holds lock X and waits for lock Y, while goroutine B holds lock Y and waits for lock X, both goroutines deadlock permanently.

This is detected by building a lock-order graph: an edge (X, Y) means "lock Y was acquired while lock X was held." A cycle in this graph indicates a potential deadlock.

## Examples

### Classic: two-lock inversion

```go
package transfer

import "sync"

type Account struct {
	mu      sync.Mutex
	balance int
}

// Transfer moves money from one account to another.
// Two goroutines calling Transfer with swapped arguments will deadlock.
func Transfer(from, to *Account, amount int) {
	from.mu.Lock()
	defer from.mu.Unlock()

	to.mu.Lock() // BUG: if another goroutine calls Transfer(to, from, ...) → deadlock
	defer to.mu.Unlock()

	from.balance -= amount
	to.balance += amount
}
```

Goroutine 1: `Transfer(accountA, accountB, 100)` — locks A then B.
Goroutine 2: `Transfer(accountB, accountA, 50)` — locks B then A.
Result: deadlock.

**golintmu output:**
```
transfer.go:15:2: potential deadlock: lock ordering cycle detected
  transfer.go:12:2: Transfer() acquires from.mu then to.mu
  When called as Transfer(A, B): order is A.mu → B.mu
  When called as Transfer(B, A): order is B.mu → A.mu
```

### Multi-struct: inconsistent ordering across methods

```go
package db

import "sync"

type DB struct {
	mu sync.Mutex
	// ...
}

type TxLog struct {
	mu sync.Mutex
	// ...
}

func (db *DB) CommitWithLog(log *TxLog) {
	db.mu.Lock()
	defer db.mu.Unlock()
	log.mu.Lock()         // order: DB.mu → TxLog.mu
	defer log.mu.Unlock()
	// ...
}

func (log *TxLog) FlushToDB(db *DB) {
	log.mu.Lock()
	defer log.mu.Unlock()
	db.mu.Lock()          // BUG: order: TxLog.mu → DB.mu (inverted)
	defer db.mu.Unlock()
	// ...
}
```

**golintmu output:**
```
db.go:24:2: potential deadlock: lock ordering cycle between DB.mu and TxLog.mu
  db.go:16:2: CommitWithLog() acquires DB.mu then TxLog.mu
  db.go:24:2: FlushToDB() acquires TxLog.mu then DB.mu
```

### Three-lock cycle

```go
package pipeline

import "sync"

type Stage struct {
	mu   sync.Mutex
	name string
}

func connect(a, b *Stage) {
	a.mu.Lock()
	b.mu.Lock() // edge: a → b
	// ...
	b.mu.Unlock()
	a.mu.Unlock()
}

// If called as:
//   connect(s1, s2)  → order: s1 → s2
//   connect(s2, s3)  → order: s2 → s3
//   connect(s3, s1)  → order: s3 → s1  → BUG: cycle s1 → s2 → s3 → s1
```

## Design Notes

- The SSA walk already records lock acquisition events with the current lock state. A lock-order graph can be built by recording `(lockA, lockB)` edges whenever `lockB` is acquired while `lockA` is held.
- Cycle detection on this directed graph (e.g., DFS-based) reveals potential deadlocks.
- Cross-package lock ordering requires exporting lock-order edges as facts.
- False positives can arise when two locks are never actually held by different goroutines simultaneously. This is hard to determine statically.
