# C14: Guarded Field Exposed Through Export

| | |
|---|---|
| **Severity** | Warning |
| **Phase** | Iteration 7 (cross-package facts) |
| **Requires** | Guard inference |
| **Interprocedural** | Cross-package |

## Description

A struct field that is inferred as guarded by a lock is exported (capitalized). This means external packages can access the field directly without knowing about the lock discipline. Since Go has no way to enforce "this field must only be accessed while holding this lock" at the language level, exporting a guarded field effectively bypasses all lock protection.

This is a warning, not an error — it nudges toward encapsulation. Cross-package fact export (future) would enable checking external accesses, but encapsulation is the safer pattern.

## Examples

### Exported guarded field

```go
package stats

import "sync"

type Stats struct {
	mu         sync.Mutex
	RequestCount int    // WARNING: guarded field is exported
	ErrorCount   int    // WARNING: guarded field is exported
	lastError    string // OK: unexported, only accessible within package
}

func (s *Stats) RecordRequest() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.RequestCount++
}

func (s *Stats) RecordError(err string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ErrorCount++
	s.lastError = err
}
```

**golintmu output:**
```
stats.go:7:2: field Stats.RequestCount is guarded by Stats.mu but is exported — external packages can bypass the lock
stats.go:8:2: field Stats.ErrorCount is guarded by Stats.mu but is exported — external packages can bypass the lock
  hint: consider making these fields unexported and providing accessor methods
```

### External package bypasses lock

```go
// In another package:
package handler

import "myapp/stats"

func handleRequest(s *stats.Stats) {
	s.RequestCount++ // BUG: no lock held — but this is in a different package
}
```

Without cross-package facts, golintmu cannot detect this. With cross-package facts (future), it would report:

```
handler.go:7:2: field Stats.RequestCount is accessed without holding Stats.mu
  Stats.RequestCount is guarded by Stats.mu (inferred in package stats)
```

### Correct pattern: encapsulate with accessor methods

```go
package stats

import "sync"

type Stats struct {
	mu           sync.Mutex
	requestCount int // unexported — can only be accessed within package
	errorCount   int
}

func (s *Stats) RequestCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requestCount
}

func (s *Stats) RecordRequest() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requestCount++
}
```

No diagnostic — guarded fields are unexported, accessor methods handle locking.

## Design Notes

- This check is a simple post-inference pass: for each field inferred as guarded, check if the field name is exported (starts with uppercase).
- Cross-package enforcement via `analysis.Fact` is planned for Iteration 7. When package B imports a type from A, golintmu imports A's `FieldGuardFact` and checks B's accesses.
- Alternative considered: only warn when `FieldGuardFact` export is also enabled, so the warning only fires when we can also check external callers. Starting with warn-always to encourage encapsulation.
