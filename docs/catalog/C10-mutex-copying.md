# C10: Mutex Copying

| | |
|---|---|
| **Severity** | Error |
| **Phase** | Future (low priority) |
| **Requires** | Type analysis |
| **Interprocedural** | No |

## Description

Copying a `sync.Mutex` or `sync.RWMutex` by value creates a separate lock instance. Code that locks the copy doesn't protect the original, breaking all synchronization guarantees. Go vet's `copylocks` already catches direct cases, but subtler ones exist.

## Examples

### Direct struct copy (caught by go vet)

```go
package counter

import "sync"

type Counter struct {
	mu    sync.Mutex
	value int
}

func snapshot(c Counter) int { // BUG: Counter passed by value — copies mu
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.value
}

func main() {
	var c Counter
	c.mu.Lock()
	c.value = 42
	c.mu.Unlock()

	v := snapshot(c) // c is copied — the copy's mu is a different lock
	_ = v
}
```

**go vet output** (already detected):
```
counter.go:10:16: snapshot passes lock by value: counter.Counter contains sync.Mutex
```

### Through interface assignment (subtler)

```go
package registry

import (
	"fmt"
	"sync"
)

type Registry struct {
	mu    sync.Mutex
	items map[string]int
}

func (r Registry) String() string { // BUG: value receiver copies Registry (and mu)
	r.mu.Lock()
	defer r.mu.Unlock()
	return fmt.Sprintf("%v", r.items)
}
```

The value receiver `(r Registry)` copies the entire struct including the mutex. Locking `r.mu` locks the copy, not the original.

**golintmu output:**
```
registry.go:14:1: method String() has value receiver on type Registry which contains sync.Mutex — consider using pointer receiver
```

### Range loop copy

```go
package tasks

import "sync"

type Task struct {
	mu   sync.Mutex
	done bool
}

func checkAll(tasks []Task) {
	for _, t := range tasks { // BUG: t is a copy of each Task
		t.mu.Lock()
		if !t.done {
			// Operating on a copy — no synchronization with the original
		}
		t.mu.Unlock()
	}
}
```

**golintmu output:**
```
tasks.go:12:12: range variable t copies tasks.Task which contains sync.Mutex
```

## Design Notes

- Go vet's `copylocks` handles most cases. golintmu could extend detection to:
  - Value receivers on types containing mutexes
  - Range loop copies
  - Interface assignment (boxing a mutex-containing struct)
- This is low priority since `copylocks` already covers the common cases.
- The analysis is purely type-based — no lock state tracking needed.
