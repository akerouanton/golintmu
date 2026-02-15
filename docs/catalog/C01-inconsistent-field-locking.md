# C1: Inconsistent Field Locking

| | |
|---|---|
| **Severity** | Error |
| **Phase** | MVP (Iteration 1) |
| **Requires** | Guard inference, lock state tracking |
| **Interprocedural** | Yes |

## Description

A struct field is accessed under a lock in some code paths but without it in others. golintmu infers which lock guards which field by observing access patterns: if a field is ever accessed while a lock is held (excluding constructors and immutable fields), the linter infers it is guarded by that lock and flags all unprotected accesses.

This is the most common mutex bug in Go codebases and the primary check golintmu performs.

## Examples

### Direct: missing lock in one method

```go
package server

import "sync"

type Server struct {
	mu      sync.Mutex
	clients map[string]*Client
}

func (s *Server) AddClient(id string, c *Client) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.clients[id] = c // locked access → golintmu infers clients is guarded by mu
}

func (s *Server) GetClient(id string) *Client {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.clients[id] // OK: mu held
}

func (s *Server) Reset() {
	s.clients = make(map[string]*Client) // BUG: s.mu not held
}
```

**golintmu output:**
```
server.go:20:2: field Server.clients is accessed without holding Server.mu
```

### Interprocedural: caller forgets to lock

```go
package server

import "sync"

type Cache struct {
	mu    sync.Mutex
	items map[string]string
}

func (c *Cache) Set(key, val string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.setLocked(key, val) // OK: mu held
}

// setLocked expects mu to be held by the caller.
func (c *Cache) setLocked(key, val string) {
	c.items[key] = val // accesses guarded field without locking itself
}

func (c *Cache) BulkSet(pairs map[string]string) {
	for k, v := range pairs {
		c.setLocked(k, v) // BUG: mu not held by caller
	}
}
```

**golintmu output:**
```
cache.go:23:3: s.mu must be held when calling c.setLocked()
  cache.go:23:3: BulkSet() calls setLocked() without holding c.mu
  cache.go:18:2: setLocked() accesses c.items (guarded by c.mu)
```

### False positive avoidance: constructor

```go
package server

import "sync"

type Counter struct {
	mu    sync.Mutex
	count int
	name  string // immutable after construction
}

func NewCounter(name string) *Counter {
	return &Counter{
		name:  name,                    // OK: constructor — excluded from inference
		count: 0,                       // OK: constructor
	}
}

func (c *Counter) Name() string {
	return c.name // OK: name is never written outside NewCounter → immutable
}

func (c *Counter) Inc() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.count++ // locked access → count is guarded by mu
}

func (c *Counter) Value() int {
	return c.count // BUG: mu not held (count is mutable and guarded)
}
```

**golintmu output:**
```
counter.go:29:9: field Counter.count is accessed without holding Counter.mu
```

No diagnostic is produced for `Name()` because `name` is immutable (only written in the constructor).

## Design Notes

- Guard inference uses "any locked access" as the threshold: if a field is accessed under a lock even once (outside constructors/init), it is considered guarded.
- For structs with multiple mutexes, inference picks the mutex most frequently held when the field is accessed.
- Immutable fields (all writes in constructors/init, only reads elsewhere) are excluded from inference.
- The mutex field itself is never inferred as guarded by itself.
