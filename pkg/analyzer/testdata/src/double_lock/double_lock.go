package double_lock

import "sync"

// --- Direct double-lock (intra-function C2) ---

type Tracker struct {
	mu    sync.Mutex
	value int
}

func (t *Tracker) DirectDoubleLock() {
	t.mu.Lock()
	t.mu.Lock() // want `Tracker\.mu is already held when locking Tracker\.mu`
	t.value = 1
	t.mu.Unlock()
	t.mu.Unlock()
}

// --- Interprocedural double-lock (caller holds, callee locks) ---

type Worker struct {
	mu   sync.Mutex
	busy bool
}

// lockAndSet locks mu and sets busy.
func (w *Worker) lockAndSet() {
	w.mu.Lock()
	w.busy = true
	w.mu.Unlock()
}

// DoubleLockViaCall holds mu and calls a function that also locks mu.
func (w *Worker) DoubleLockViaCall() {
	w.mu.Lock()
	w.lockAndSet() // want `Worker\.mu is already held when calling lockAndSet\(\) which locks Worker\.mu`
	w.mu.Unlock()
}

// --- Deep transitive double-lock ---

type Manager struct {
	mu   sync.Mutex
	data string
}

// innerLock locks mu and modifies data.
func (m *Manager) innerLock() {
	m.mu.Lock()
	m.data = "inner"
	m.mu.Unlock()
}

// middleWrapper calls innerLock (transitively acquires mu).
func (m *Manager) middleWrapper() {
	m.innerLock()
}

// DeepDoubleLock holds mu and calls through a chain that also locks mu.
func (m *Manager) DeepDoubleLock() {
	m.mu.Lock()
	m.middleWrapper() // want `Manager\.mu is already held when calling middleWrapper\(\) which locks Manager\.mu`
	m.mu.Unlock()
}

// --- Recursive double-lock ---

type Recurser struct {
	mu   sync.Mutex
	val  int
}

// RecursiveDoubleLock holds mu and calls itself recursively.
func (r *Recurser) RecursiveDoubleLock(n int) {
	if n <= 0 {
		return
	}
	r.mu.Lock()
	r.RecursiveDoubleLock(n - 1) // want `Recurser\.mu is already held when calling RecursiveDoubleLock\(\) which locks Recurser\.mu`
	r.mu.Unlock()
}
