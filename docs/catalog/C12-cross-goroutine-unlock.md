# C12: Lock/Unlock in Different Goroutines

| | |
|---|---|
| **Severity** | Warning |
| **Phase** | Future (hard) |
| **Requires** | Lock state tracking, goroutine ownership tracking |
| **Interprocedural** | Yes |

## Description

A mutex is locked in one goroutine but unlocked in another. While technically allowed by Go's `sync.Mutex` (unlike some languages where lock ownership is per-thread), this pattern is fragile, hard to reason about, and almost always a design smell. It makes it impossible to use `defer` for unlock, and any intermediate panic leaves the lock in an inconsistent state.

## Examples

### Unlock delegated to spawned goroutine

```go
package pipeline

import "sync"

type Pipeline struct {
	mu   sync.Mutex
	data []byte
}

func (p *Pipeline) StartProcessing() {
	p.mu.Lock()
	p.data = fetchData()

	go func() {
		process(p.data)
		p.mu.Unlock() // WARNING: unlocking in a different goroutine than where Lock() was called
	}()
	// Caller returns immediately — lock owned by spawned goroutine.
	// If process() panics, mu is never unlocked.
}
```

**golintmu output:**
```
pipeline.go:15:3: p.mu.Unlock() in goroutine spawned at pipeline.go:13 — lock was acquired in parent goroutine at pipeline.go:10
  This pattern prevents using defer and is fragile if the goroutine panics.
```

### Unlock via channel signaling

```go
package gate

import "sync"

type Gate struct {
	mu     sync.Mutex
	ready  chan struct{}
}

func (g *Gate) Open() {
	g.mu.Lock()
	g.ready <- struct{}{} // Signal another goroutine that it can proceed
	// The other goroutine is expected to unlock g.mu — fragile pattern
}

func (g *Gate) WaitAndClose() {
	<-g.ready
	// ... do work assuming lock is held ...
	g.mu.Unlock() // WARNING: unlocking a lock acquired in a different goroutine
}
```

**golintmu output:**
```
gate.go:18:2: g.mu.Unlock() called but g.mu was not locked in this goroutine
  g.mu was locked in Open() at gate.go:11 — consider restructuring to lock/unlock in the same goroutine
```

## Design Notes

- This is one of the hardest checks to implement statically because it requires tracking which goroutine owns which lock.
- Static analysis can detect the pattern when:
  - A lock is acquired before a `go` statement
  - The lock is not unlocked before or deferred in the locking goroutine
  - The spawned goroutine contains an unlock of the same lock
- False negatives are likely for complex patterns (e.g., unlock delegated through channels or shared state).
- Low priority — this pattern is rare in well-written Go code.
