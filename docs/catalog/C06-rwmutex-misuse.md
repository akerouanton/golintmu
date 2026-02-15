# C6: RWMutex Misuse

| | |
|---|---|
| **Severity** | Error / Warning |
| **Phase** | Future (Iteration 9) |
| **Requires** | Lock state tracking with lock-level distinction |
| **Interprocedural** | Yes |

## Description

`sync.RWMutex` supports both exclusive locks (`Lock`/`Unlock`) and shared read locks (`RLock`/`RUnlock`). Several classes of misuse exist:

- **Mismatched unlock**: Calling `Unlock()` after `RLock()`, or `RUnlock()` after `Lock()`. Both cause runtime panics or undefined behavior.
- **Recursive RLock**: Calling `RLock()` while already holding `RLock()`. This can deadlock if a writer calls `Lock()` between the two `RLock()` calls, because `Lock()` blocks new readers.
- **Unnecessary exclusive lock**: Using `Lock()` for read-only access when `RLock()` would suffice. Not a correctness bug, but a performance issue — it serializes readers unnecessarily.

## Examples

### Mismatched unlock: Unlock after RLock

```go
package config

import "sync"

type Config struct {
	rw     sync.RWMutex
	values map[string]string
}

func (c *Config) Get(key string) string {
	c.rw.RLock()
	defer c.rw.Unlock() // BUG: should be RUnlock() — Unlock() after RLock() is undefined
	return c.values[key]
}
```

**golintmu output:**
```
config.go:12:8: c.rw.Unlock() called but c.rw was read-locked with RLock() — use RUnlock() instead
```

### Mismatched unlock: RUnlock after Lock

```go
package state

import "sync"

type State struct {
	rw   sync.RWMutex
	data []byte
}

func (s *State) Update(data []byte) {
	s.rw.Lock()
	defer s.rw.RUnlock() // BUG: should be Unlock() — RUnlock() after Lock() is undefined
	s.data = data
}
```

**golintmu output:**
```
state.go:12:8: s.rw.RUnlock() called but s.rw was exclusively locked with Lock() — use Unlock() instead
```

### Recursive RLock (potential deadlock)

```go
package tree

import "sync"

type Tree struct {
	rw       sync.RWMutex
	children []*Tree
	value    int
}

func (t *Tree) Sum() int {
	t.rw.RLock()
	defer t.rw.RUnlock()

	total := t.value
	for _, child := range t.children {
		total += child.Sum() // If child == t (cycle), this RLock()s again
		// Even without cycles: if another goroutine calls Lock() between
		// the parent RLock and child RLock, the child RLock blocks → deadlock
	}
	return total
}
```

**golintmu output:**
```
tree.go:17:13: recursive RLock on t.rw — can deadlock if a writer is waiting
  tree.go:11:2: first RLock at tree.go:11
  tree.go:17:13: Sum() calls child.Sum() which RLocks the same mutex
```

### Unnecessary exclusive lock (performance warning)

```go
package metrics

import "sync"

type Metrics struct {
	rw      sync.RWMutex
	counts  map[string]int64
}

func (m *Metrics) Get(name string) int64 {
	m.rw.Lock()         // WARNING: read-only access could use RLock()
	defer m.rw.Unlock()
	return m.counts[name]
}
```

**golintmu output:**
```
metrics.go:11:2: m.rw.Lock() used for read-only access to m.counts — consider using RLock() for better concurrency
```

## Design Notes

- Requires extending `lockState` to track the lock level: exclusive (`Lock`) vs. read (`RLock`).
- The `heldLock` type already has an `Exclusive` field for this.
- Mismatched unlock detection: when `Unlock()` is called, check if the lock was acquired with `RLock()` (and vice versa).
- Recursive RLock detection: when `RLock()` is called and the lock is already in the held set as read-locked, report.
- Unnecessary exclusive lock: when all field accesses within a `Lock()`/`Unlock()` critical section are reads, suggest `RLock()`.
