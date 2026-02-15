# C7: Deferred Lock Instead of Unlock

| | |
|---|---|
| **Severity** | Error |
| **Phase** | Future |
| **Requires** | Lock state tracking, AST pattern match |
| **Interprocedural** | No |

## Description

A common typo: writing `defer mu.Lock()` instead of `defer mu.Unlock()`. This means the mutex is locked immediately and then locked again when the deferred call runs at function exit — causing a deadlock. Already detected by staticcheck (SA2003), but trivial to include since golintmu is already tracking lock calls.

## Examples

### Classic typo

```go
package session

import "sync"

type SessionStore struct {
	mu       sync.Mutex
	sessions map[string]*Session
}

func (s *SessionStore) Get(id string) *Session {
	s.mu.Lock()
	defer s.mu.Lock() // BUG: should be defer s.mu.Unlock()

	return s.sessions[id]
}
```

At function exit, `defer s.mu.Lock()` fires, attempting to lock an already-held mutex. The goroutine deadlocks.

**golintmu output:**
```
session.go:12:8: defer s.mu.Lock() will deadlock — did you mean defer s.mu.Unlock()?
```

### With RWMutex

```go
package index

import "sync"

type Index struct {
	rw    sync.RWMutex
	items []string
}

func (idx *Index) Search(query string) []string {
	idx.rw.RLock()
	defer idx.rw.RLock() // BUG: should be defer idx.rw.RUnlock()

	var results []string
	for _, item := range idx.items {
		if matches(item, query) {
			results = append(results, item)
		}
	}
	return results
}
```

**golintmu output:**
```
index.go:12:8: defer idx.rw.RLock() will deadlock — did you mean defer idx.rw.RUnlock()?
```

## Design Notes

- In the SSA walk, when processing a `*ssa.Defer` instruction that calls `Lock()` or `RLock()`, check if the same mutex was already locked in the current function. If so, this is almost certainly a typo.
- Can also be detected purely syntactically: a `defer` statement calling `Lock()` on a mutex is almost never intentional.
- This is one of the simplest checks to implement and has a near-zero false positive rate.
