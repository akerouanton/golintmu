# C2: Double Locking

| | |
|---|---|
| **Severity** | Error |
| **Phase** | MVP (Iteration 4) |
| **Requires** | Lock state tracking, interprocedural analysis |
| **Interprocedural** | Yes |

## Description

A `sync.Mutex` is locked when it's already held on the same code path. Since Go's mutexes are non-recursive, this causes an immediate deadlock — the goroutine blocks forever waiting to acquire a lock it already holds.

This commonly happens when a public method acquires a lock and calls a private method that also acquires the same lock, or when a method calls itself recursively while holding a lock.

## Examples

### Direct: method calls another locking method

```go
package registry

import "sync"

type Registry struct {
	mu    sync.Mutex
	items map[string]int
}

func (r *Registry) Set(key string, val int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.items[key] = val
}

func (r *Registry) SetDefault(key string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Set(key, 0) // BUG: Set() calls r.mu.Lock() but mu is already held → deadlock
}
```

**golintmu output:**
```
registry.go:18:2: r.mu is already held when calling r.Set() which locks r.mu
  registry.go:18:2: SetDefault() holds r.mu
  registry.go:11:2: Set() locks r.mu
```

### Interprocedural: deep call chain

```go
package pool

import "sync"

type Pool struct {
	mu      sync.Mutex
	workers []*Worker
}

func (p *Pool) Resize(n int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.adjustWorkers(n)
}

func (p *Pool) adjustWorkers(n int) {
	if n > len(p.workers) {
		p.addWorkers(n - len(p.workers))
	}
}

func (p *Pool) addWorkers(count int) {
	for i := 0; i < count; i++ {
		p.mu.Lock() // BUG: mu already held by Resize() → deadlock
		p.workers = append(p.workers, newWorker())
		p.mu.Unlock()
	}
}
```

**golintmu output:**
```
pool.go:23:3: p.mu is already held when locking p.mu
  pool.go:12:2: Resize() locks p.mu
  pool.go:17:2: adjustWorkers() called while p.mu is held
  pool.go:23:3: addWorkers() locks p.mu again
```

### Conditional double-lock

```go
package store

import "sync"

type Store struct {
	mu   sync.Mutex
	data map[string]string
}

func (s *Store) GetOrCreate(key string) string {
	s.mu.Lock()
	defer s.mu.Unlock()

	if val, ok := s.data[key]; ok {
		return val
	}
	s.init() // BUG: init() also locks mu
	return s.data[key]
}

func (s *Store) init() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.data == nil {
		s.data = make(map[string]string)
	}
}
```

**golintmu output:**
```
store.go:17:2: s.mu is already held when calling s.init() which locks s.mu
  store.go:11:2: GetOrCreate() holds s.mu
  store.go:21:2: init() locks s.mu
```

## Design Notes

- Detection relies on the lock state tracker: when a `Lock()` call is encountered and the `lockState` already contains that lock, report a double-lock.
- Interprocedural detection uses function lock facts: if a callee is known to acquire lock X, and the caller already holds X, report.
- This check does not require guard inference — it only needs lock state tracking.
- Recursive functions that lock on each call are a special case and always flagged.
