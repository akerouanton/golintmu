# C11: Inconsistent Lock State Across Branches

| | |
|---|---|
| **Severity** | Error |
| **Phase** | Future (Iteration 3) |
| **Requires** | Lock state tracking, COW fork + merge |
| **Interprocedural** | No |

## Description

A function acquires a lock in one branch of a conditional but not the other, then proceeds to execute common code after the branch merge point. The lock state is inconsistent at the merge — held in one predecessor, not held in the other. Any field access or unlock after the merge is unsafe on at least one path.

## Examples

### Lock in one branch, access after merge

```go
package store

import "sync"

type Store struct {
	mu   sync.Mutex
	data map[string]string
}

func (s *Store) Get(key string, locked bool) string {
	if locked {
		s.mu.Lock()
	}

	val := s.data[key] // BUG: mu only held when locked=true

	if locked {
		s.mu.Unlock()
	}
	return val
}
```

**golintmu output:**
```
store.go:15:8: inconsistent lock state: s.mu is held on one branch but not the other
  store.go:11:3: s.mu locked when locked=true
  store.go:15:8: s.data accessed — s.mu not held when locked=false
```

### Conditional lock with unconditional unlock

```go
package cache

import "sync"

type Cache struct {
	mu       sync.Mutex
	entries  map[string]string
	readonly bool
}

func (c *Cache) Set(key, val string) {
	if !c.readonly {
		c.mu.Lock()
	}

	c.entries[key] = val // BUG: not locked when readonly=true

	c.mu.Unlock() // BUG: unlocking when not locked if readonly=true → panic
}
```

**golintmu output:**
```
cache.go:16:2: inconsistent lock state: c.mu may not be held at this point
  cache.go:12:3: c.mu locked only when !c.readonly
  cache.go:16:2: c.entries accessed — c.mu not held when c.readonly=true
cache.go:18:2: c.mu.Unlock() called but c.mu may not be held (see C4)
```

### Lock in both branches with different mutexes

```go
package dual

import "sync"

type DualStore struct {
	readMu  sync.Mutex
	writeMu sync.Mutex
	data    map[string]string
}

func (d *DualStore) Access(key string, write bool) string {
	if write {
		d.writeMu.Lock()
	} else {
		d.readMu.Lock()
	}

	val := d.data[key] // Inconsistent: guarded by writeMu on one path, readMu on another

	if write {
		d.writeMu.Unlock()
	} else {
		d.readMu.Unlock()
	}
	return val
}
```

**golintmu output:**
```
dual.go:18:8: inconsistent lock state at merge point: d.writeMu held on one branch, d.readMu on another
```

## Design Notes

- Detected via COW fork + merge: at CFG merge points (blocks with multiple predecessors), compare the lock states from each predecessor.
- If the lock states differ (a lock is held in one predecessor but not the other), the merge point has inconsistent state.
- This falls out naturally from the branch-forking design in Iteration 3.
- Related to C4 (unlock of unlocked) and C5 (lock leak) — inconsistent branches often lead to both.
