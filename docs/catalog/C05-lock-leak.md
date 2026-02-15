# C5: Lock Leak / Missing Unlock

| | |
|---|---|
| **Severity** | Error |
| **Phase** | Future |
| **Requires** | Lock state tracking, CFG path analysis |
| **Interprocedural** | No (initially) |

## Description

A function acquires a lock but has a code path where it returns without releasing it. This leaves the mutex permanently locked, causing every subsequent lock attempt to deadlock. The most common cause is early returns (error handling) without using `defer`.

## Examples

### Early return without defer

```go
package db

import (
	"errors"
	"sync"
)

type DB struct {
	mu     sync.Mutex
	conn   *Connection
	closed bool
}

func (db *DB) Query(sql string) (*Result, error) {
	db.mu.Lock()

	if db.closed {
		return nil, errors.New("db is closed") // BUG: mu not unlocked on this path
	}

	result := db.conn.Execute(sql)
	db.mu.Unlock()
	return result, nil
}
```

**golintmu output:**
```
db.go:19:3: return without unlocking db.mu (locked at db.go:16:2)
  hint: consider using defer db.mu.Unlock() after Lock()
```

### Multiple early returns

```go
package validator

import "sync"

type Validator struct {
	mu    sync.Mutex
	rules []Rule
	cache map[string]bool
}

func (v *Validator) Validate(input string) error {
	v.mu.Lock()

	if cached, ok := v.cache[input]; ok {
		if cached {
			return nil // BUG: mu not unlocked
		}
		return errors.New("cached invalid") // BUG: mu not unlocked
	}

	for _, rule := range v.rules {
		if err := rule.Check(input); err != nil {
			v.cache[input] = false
			return err // BUG: mu not unlocked
		}
	}

	v.cache[input] = true
	v.mu.Unlock()
	return nil
}
```

**golintmu output:**
```
validator.go:16:4: return without unlocking v.mu (locked at validator.go:13:2)
validator.go:18:3: return without unlocking v.mu (locked at validator.go:13:2)
validator.go:23:4: return without unlocking v.mu (locked at validator.go:13:2)
  hint: consider using defer v.mu.Unlock() after Lock()
```

### Correct pattern (no diagnostic)

```go
package db

import "sync"

type SafeDB struct {
	mu   sync.Mutex
	conn *Connection
}

func (db *SafeDB) Query(sql string) (*Result, error) {
	db.mu.Lock()
	defer db.mu.Unlock() // defer ensures unlock on all paths

	if db.conn == nil {
		return nil, errors.New("not connected")
	}
	return db.conn.Execute(sql), nil
}
```

No diagnostic — `defer` guarantees the unlock.

## Design Notes

- At every return instruction in the SSA CFG, check `lockState` for locks acquired in this function that aren't released and don't have a pending deferred unlock.
- Deferred unlocks are tracked separately: when `defer mu.Unlock()` is encountered, the lock is marked as "will be released at function exit."
- Panics that propagate through the function also trigger deferred calls, so `defer` correctly handles panic paths too.
- This check does not require guard inference — it only needs lock state tracking through control flow.
