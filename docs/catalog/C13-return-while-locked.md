# C13: Returning While Holding a Lock Without Caller Awareness

| | |
|---|---|
| **Severity** | Warning |
| **Phase** | Future |
| **Requires** | Lock state tracking, interprocedural facts |
| **Interprocedural** | Yes |

## Description

A function acquires a lock and returns without releasing it, effectively transferring lock ownership to the caller. Unless the caller explicitly expects this (i.e., the function is an intentional "acquire" helper), this is a bug — the caller doesn't know it needs to unlock.

This is related to C5 (lock leak) but focuses on the interprocedural aspect: the lock is not leaked within the function (it's intentionally held at return), but the caller may not handle it correctly.

## Examples

### Accidental return with lock held

```go
package auth

import "sync"

type AuthCache struct {
	mu    sync.Mutex
	users map[string]*User
}

func (a *AuthCache) GetOrLoad(id string) (*User, error) {
	a.mu.Lock()

	if user, ok := a.users[id]; ok {
		return user, nil // BUG: returns with a.mu still held — caller doesn't know to unlock
	}

	user, err := loadUser(id)
	if err != nil {
		a.mu.Unlock()
		return nil, err
	}

	a.users[id] = user
	a.mu.Unlock()
	return user, nil
}
```

**golintmu output:**
```
auth.go:14:3: returning while holding a.mu (locked at auth.go:11) — caller is not aware it must unlock
  hint: add defer a.mu.Unlock() after Lock(), or unlock before the early return
```

### Intentional acquire pattern (should not warn if documented)

```go
package kvstore

import "sync"

type KVStore struct {
	mu   sync.Mutex
	data map[string]string
}

// AcquireForBatch locks the store and returns it for batch operations.
// The caller MUST call mu.Unlock() when done.
//
//mu:ignore
func (kv *KVStore) AcquireForBatch() {
	kv.mu.Lock()
	// Intentionally returns with lock held — caller will unlock.
}

func (kv *KVStore) BatchUpdate(pairs map[string]string) {
	kv.AcquireForBatch()
	defer kv.mu.Unlock()

	for k, v := range pairs {
		kv.data[k] = v
	}
}
```

No diagnostic on `AcquireForBatch` because of `//mu:ignore`. The caller `BatchUpdate` correctly unlocks.

### Unaware caller

```go
package registry

import "sync"

type Registry struct {
	mu    sync.Mutex
	items map[string]Item
}

func (r *Registry) lockAndGet(key string) *Item {
	r.mu.Lock()
	item := r.items[key]
	return &item // Returns with r.mu held
}

func (r *Registry) Process(key string) {
	item := r.lockAndGet(key)
	// ... use item ...
	// BUG: r.mu is still held — never unlocked
}
```

**golintmu output:**
```
registry.go:11:1: lockAndGet() returns while holding r.mu — callers must unlock
registry.go:16:9: Process() calls lockAndGet() which acquires r.mu, but Process() never releases it
```

## Design Notes

- At return points, check if the function holds locks it acquired (not passed in by caller).
- If the function is annotated with `//mu:ignore` or has documented acquire semantics, suppress.
- Function lock facts (`FuncLockFact`) model this: a function that acquires a lock and doesn't release it has an "acquires" postcondition.
- Callers of such functions must either release the lock or propagate the postcondition.
- Related to C5 (lock leak) — C5 focuses on missing unlock paths within a function, C13 focuses on the caller-callee contract.
