# golintmu

A Go static analysis linter that detects inconsistent mutex locking of struct fields. Unlike annotation-based tools (e.g. gVisor's checklocks), golintmu **infers** which lock guards which field by observing access patterns across your codebase -- no `+checklocks:mu` annotations needed.

Built on `golang.org/x/tools/go/analysis` and SSA.

## Installation

```bash
go install github.com/akerouanton/golintmu/cmd/golintmu@latest
```

Requires Go 1.24+.

## Usage

### Standalone

```bash
golintmu ./...
```

### With `go vet`

```bash
go vet -vettool=$(which golintmu) ./...
```

### Output

golintmu produces standard Go diagnostics:

```
server.go:18:9: field Counter.count is accessed without holding Counter.mu
handler.go:42:2: Counter.mu must be held when calling increment()
transfer.go:49:2: potential deadlock: lock ordering cycle on Account.mu
config.go:14:9: Config.rw is read-locked but Unlock() was called -- use RUnlock()
worker.go:25:2: return without unlocking Worker.mu (locked at worker.go:21:2)
```

## What It Detects

### Inconsistent field locking

Fields accessed under a lock in some methods but not others:

```go
type Counter struct {
    mu    sync.Mutex
    count int
}

func (c *Counter) Inc() {
    c.mu.Lock()
    c.count++          // locked -- golintmu infers mu guards count
    c.mu.Unlock()
}

func (c *Counter) Get() int {
    return c.count     // ERROR: field Counter.count is accessed without holding Counter.mu
}
```

### Interprocedural analysis

Lock requirements propagate through call chains. If a helper accesses a guarded field without holding the lock, callers that don't hold the lock are flagged:

```go
func (c *Counter) increment() {
    c.count++          // requires mu (propagated to callers)
}

func (c *Counter) SafeInc() {
    c.mu.Lock()
    c.increment()      // OK: mu is held
    c.mu.Unlock()
}

func (c *Counter) UnsafeInc() {
    c.increment()      // ERROR: Counter.mu must be held when calling increment()
}
```

### Double locking

Detects immediate deadlocks from locking a mutex that is already held, including through call chains:

```go
func (w *Worker) lockAndSet() {
    w.mu.Lock()
    w.busy = true
    w.mu.Unlock()
}

func (w *Worker) DoubleLockViaCall() {
    w.mu.Lock()
    w.lockAndSet()     // ERROR: Worker.mu is already held when calling lockAndSet()
    w.mu.Unlock()
}
```

### Lock ordering violations

Detects deadlock risks from inconsistent lock acquisition order:

```go
func (d *DB) CommitWithLog(log *TxLog) {
    d.mu.Lock()
    log.mu.Lock()      // ERROR: potential deadlock: lock ordering cycle between DB.mu and TxLog.mu
    // ...
}

func (d *DB) FlushToDB(log *TxLog) {
    log.mu.Lock()
    d.mu.Lock()        // opposite order
    // ...
}
```

### Unlock of unlocked mutex

Catches unpaired unlock calls that would panic at runtime:

```go
func (t *Tracker) DirectDoubleLock() {
    t.mu.Lock()
    t.mu.Lock()        // ERROR: already held
    t.mu.Unlock()
    t.mu.Unlock()      // ERROR: Unlock() called but Tracker.mu is not held
}
```

### Lock leak / missing unlock

Detects functions that return while still holding a lock:

```go
func (e *EarlyReturn) Process(cond bool) error {
    e.mu.Lock()
    if cond {
        return nil     // ERROR: return without unlocking EarlyReturn.mu
    }
    e.value = 42
    e.mu.Unlock()
    return nil
}
```

### RWMutex misuse

Catches mismatched lock/unlock pairs, writes under read locks, recursive `RLock`, and lock upgrades:

```go
func (c *Config) Get(key string) string {
    c.rw.RLock()
    defer c.rw.Unlock()   // ERROR: read-locked but Unlock() was called -- use RUnlock()
    return c.values[key]
}

func (w *WriteUnderRLock) BadWrite(v int) {
    w.rw.RLock()
    w.value = v            // ERROR: written while rw is read-locked -- use Lock()
    w.rw.RUnlock()
}

func (r *Registry) GetOrCreate(key string) int {
    r.rw.RLock()
    r.rw.Lock()            // ERROR: lock upgrade can deadlock
    // ...
}
```

### Inconsistent branch locking

Detects lock state that differs across branches:

```go
func (s *Server) Handle(cond bool) {
    if cond {
        s.mu.Lock()
    }
    s.count++              // ERROR: inconsistent lock state across branches
}
```

### Exported guarded field

Warns when a guarded field is exported, since external code can bypass the lock:

```go
type Cache struct {
    mu    sync.Mutex
    Items map[string]string // WARNING: guarded field Items is exported
}
```

## Annotations

golintmu works without annotations, but provides directives for edge cases:

### `//mu:concurrent`

Marks a function as a concurrent entrypoint. golintmu automatically detects `go` statements, `ServeHTTP`, and `http.HandleFunc` callbacks, but use this when concurrency isn't visible to the analyzer:

```go
//mu:concurrent
func (s *Server) HandleRequest(r *Request) {
    // called from external framework
}
```

### `//mu:ignore`

Suppresses all diagnostics within a function. Use for intentional patterns like functions that acquire a lock and return without unlocking (expecting the caller to unlock):

```go
//mu:ignore
func (s *Server) acquireLock() {
    s.mu.Lock()
    // intentional: caller will unlock
}
```

### `//mu:nolint`

Suppresses the diagnostic on the next line only:

```go
//mu:nolint
s.count++ // no diagnostic reported here
```

## How It Works

golintmu runs as a single `analysis.Analyzer` with five sequential phases:

1. **Observation Collection** -- Walks the SSA control-flow graph of every function, tracking which mutexes are held at each program point. At each struct field read or write, records which mutex fields on the same struct are held.

2. **Guard Inference** -- For each struct field, looks at all observations and infers the guard as the mutex most frequently held during access. Excludes constructors (`New*`, `Make*`, `Create*`), `init()`, and immutable fields (written only in constructors).

3. **Requirement Propagation** -- Bottom-up fixed-point iteration through the call graph. If a function accesses a guarded field without holding the lock, it inherits a lock requirement. Callers that don't satisfy the requirement are flagged.

4. **Concurrent Context Detection** -- Identifies concurrent entrypoints (`go` statements, `ServeHTTP`, `//mu:concurrent`) and computes reachability. Only reports violations in functions reachable from concurrent contexts.

5. **Violation Detection** -- Re-examines all field accesses and call sites. Reports diagnostics where a guarded field is accessed or a function with lock requirements is called without the necessary lock held.

Additional sub-phases handle lock-order cycle detection, unlock-of-unlocked, and lock-leak analysis.

## Cross-Package Analysis

golintmu exports facts about guarded fields and function lock requirements via the `go/analysis` fact system. When analyzing package B that imports package A, golintmu knows which fields in A are guarded and which functions in A require locks to be held, enabling cross-package violation detection.

## False Positive Mitigation

golintmu uses several strategies to minimize noise:

- **Constructor exclusion** -- Fields set in `New*`/`Make*`/`Create*` functions and `init()` are excluded from guard inference
- **Immutability detection** -- Fields written only in constructors and read elsewhere are not flagged
- **Concurrent context filtering** -- Only reports violations in functions reachable from concurrent entrypoints
- **Test file exclusion** -- Skips `_test.go` files by default

## Known Limitations

- Interface method calls are treated as opaque (locks across interfaces are not tracked)
- No `sync.Once` awareness (may produce false positives on lazy initialization)
- No atomic or channel-based synchronization awareness
- Lock wrapper functions (callbacks under lock) are not understood
- Constructor detection is heuristic-based (`New*`/`Make*`/`Create*` prefix + return-type analysis)

## License

[MIT](LICENSE)
