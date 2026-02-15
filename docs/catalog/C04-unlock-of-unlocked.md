# C4: Unlock of Unlocked Mutex

| | |
|---|---|
| **Severity** | Error |
| **Phase** | Future |
| **Requires** | Lock state tracking |
| **Interprocedural** | No (initially) |

## Description

Calling `Unlock()` on a mutex that isn't currently held causes a runtime panic: `panic: sync: unlock of unlocked mutex`. This can happen from unmatched unlock calls, copy-paste errors, or incorrect control flow.

## Examples

### Direct: unpaired unlock

```go
package counter

import "sync"

type Counter struct {
	mu    sync.Mutex
	value int
}

func (c *Counter) Reset() {
	c.value = 0
	c.mu.Unlock() // BUG: mu was never locked → panic at runtime
}
```

**golintmu output:**
```
counter.go:12:2: c.mu.Unlock() called but c.mu is not held
```

### Double unlock

```go
package cache

import "sync"

type Cache struct {
	mu   sync.Mutex
	data map[string]string
}

func (c *Cache) Clear() {
	c.mu.Lock()
	c.data = make(map[string]string)
	c.mu.Unlock()
	c.mu.Unlock() // BUG: mu already unlocked → panic
}
```

**golintmu output:**
```
cache.go:14:2: c.mu.Unlock() called but c.mu is not held (already unlocked at cache.go:13:2)
```

### Unlock in wrong branch

```go
package worker

import "sync"

type Worker struct {
	mu      sync.Mutex
	running bool
}

func (w *Worker) Stop(force bool) {
	if force {
		w.mu.Lock()
		w.running = false
	}
	w.mu.Unlock() // BUG: when force=false, mu was never locked → panic
}
```

**golintmu output:**
```
worker.go:15:2: w.mu.Unlock() called but w.mu may not be held (only locked when force=true)
```

## Design Notes

- Falls out naturally from `lockState.unlock()`: if the lock isn't in the held set, report.
- The branch case (unlock in one path but not locked in another) is detected via COW fork + merge analysis.
- This is closely related to C11 (inconsistent lock state across branches).
