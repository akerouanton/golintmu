# C8: Holding Lock Across Goroutine Spawn

| | |
|---|---|
| **Severity** | Warning |
| **Phase** | Future |
| **Requires** | Lock state tracking, `go` statement detection |
| **Interprocedural** | No |

## Description

Spawning a goroutine while holding a lock is suspicious. The spawned goroutine runs concurrently with the caller, but the lock is owned by the caller's goroutine. The new goroutine cannot rely on the lock being held, and the caller may hold the lock for longer than intended — or the spawned goroutine may attempt to acquire the same lock, causing a deadlock.

## Examples

### Spawned goroutine assumes lock is held

```go
package pool

import "sync"

type Pool struct {
	mu      sync.Mutex
	workers []*Worker
	tasks   chan Task
}

func (p *Pool) Scale(n int) {
	p.mu.Lock()
	defer p.mu.Unlock()

	for i := len(p.workers); i < n; i++ {
		w := &Worker{id: i}
		p.workers = append(p.workers, w)
		go w.Run(p.tasks) // WARNING: goroutine spawned while p.mu is held
	}
}
```

While `w.Run` doesn't access `p.workers`, holding the lock during goroutine creation means the lock is held for longer than necessary. If `Run` ever accesses `p.mu`, it would deadlock.

**golintmu output:**
```
pool.go:18:3: goroutine spawned while holding p.mu
```

### Spawned goroutine tries to re-lock

```go
package broadcaster

import "sync"

type Broadcaster struct {
	mu        sync.Mutex
	listeners []chan string
}

func (b *Broadcaster) Broadcast(msg string) {
	b.mu.Lock()
	defer b.mu.Unlock()

	for _, ch := range b.listeners {
		go func(c chan string) {
			// This goroutine might outlive Broadcast() — that's fine.
			// But if it ever calls b.AddListener(), deadlock.
			c <- msg
		}(ch) // WARNING: goroutine spawned while b.mu is held
	}
}

func (b *Broadcaster) AddListener() chan string {
	b.mu.Lock()
	defer b.mu.Unlock()
	ch := make(chan string, 1)
	b.listeners = append(b.listeners, ch)
	return ch
}
```

**golintmu output:**
```
broadcaster.go:15:3: goroutine spawned while holding b.mu
```

### Correct pattern: spawn outside lock

```go
package broadcaster

import "sync"

type SafeBroadcaster struct {
	mu        sync.Mutex
	listeners []chan string
}

func (b *SafeBroadcaster) Broadcast(msg string) {
	b.mu.Lock()
	snapshot := make([]chan string, len(b.listeners))
	copy(snapshot, b.listeners)
	b.mu.Unlock()

	// Goroutines spawned outside the lock — no issue
	for _, ch := range snapshot {
		go func(c chan string) { c <- msg }(ch)
	}
}
```

No diagnostic — lock is released before goroutine spawn.

## Design Notes

- When processing `*ssa.Go` instructions, check if `lockState` has any held locks. If so, report a warning.
- This is a warning, not an error — there are legitimate (if uncommon) patterns where this is intentional.
- The check does not require guard inference — only lock state tracking.
