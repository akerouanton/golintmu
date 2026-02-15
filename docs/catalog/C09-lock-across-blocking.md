# C9: Holding Lock Across Blocking Operations

| | |
|---|---|
| **Severity** | Warning |
| **Phase** | Future |
| **Requires** | Lock state tracking, blocking-call detection |
| **Interprocedural** | No (initially) |

## Description

Holding a lock while performing a blocking operation (channel send/receive, `select`, `time.Sleep`, network I/O) risks deadlocks or severe contention. The lock is held for an unbounded duration, preventing other goroutines from making progress.

## Examples

### Channel send while holding lock

```go
package dispatcher

import "sync"

type Dispatcher struct {
	mu     sync.Mutex
	queue  []Job
	notify chan struct{}
}

func (d *Dispatcher) Enqueue(job Job) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.queue = append(d.queue, job)
	d.notify <- struct{}{} // WARNING: channel send while holding d.mu
	// If notify is full/unbuffered, this blocks while holding the lock.
	// Any goroutine that needs d.mu to drain the channel → deadlock.
}
```

**golintmu output:**
```
dispatcher.go:16:2: channel send while holding d.mu — may block indefinitely
```

### Select with channels while locked

```go
package coordinator

import (
	"sync"
	"time"
)

type Coordinator struct {
	mu    sync.Mutex
	state string
	done  chan struct{}
}

func (c *Coordinator) WaitForDone() {
	c.mu.Lock()
	defer c.mu.Unlock()

	select {
	case <-c.done: // WARNING: blocking select while holding c.mu
		c.state = "done"
	case <-time.After(5 * time.Second):
		c.state = "timeout"
	}
}
```

**golintmu output:**
```
coordinator.go:19:2: blocking select while holding c.mu — may hold lock for up to 5s
```

### Sleep while holding lock

```go
package ratelimiter

import (
	"sync"
	"time"
)

type RateLimiter struct {
	mu       sync.Mutex
	tokens   int
	interval time.Duration
}

func (r *RateLimiter) Wait() {
	r.mu.Lock()
	defer r.mu.Unlock()

	for r.tokens <= 0 {
		time.Sleep(r.interval) // WARNING: sleeping while holding r.mu
	}
	r.tokens--
}
```

**golintmu output:**
```
ratelimiter.go:19:3: time.Sleep() called while holding r.mu — blocks all other lock acquisitions
```

### Correct pattern: release lock before blocking

```go
package dispatcher

import "sync"

type SafeDispatcher struct {
	mu     sync.Mutex
	queue  []Job
	notify chan struct{}
}

func (d *SafeDispatcher) Enqueue(job Job) {
	d.mu.Lock()
	d.queue = append(d.queue, job)
	d.mu.Unlock()

	d.notify <- struct{}{} // OK: lock released before blocking send
}
```

No diagnostic — lock is released before the blocking operation.

## Design Notes

- Detect channel operations (`*ssa.Send`, `*ssa.Select`, `*ssa.UnOp` for receive) while locks are held.
- Detect known blocking calls: `time.Sleep`, `(*net.Conn).Read`, `(*net.Conn).Write`, `(*http.Client).Do`, etc.
- This is a warning, not an error — some short-duration blocking operations inside locks are acceptable (e.g., buffered channel sends that rarely block).
- The set of recognized blocking operations should be configurable/extensible.
