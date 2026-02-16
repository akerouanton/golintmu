package lock_ordering

import "sync"

// --- Two-lock inversion: CommitWithLog locks DB then TxLog; FlushToDB locks TxLog then DB ---

type DB struct {
	mu   sync.Mutex
	data string
}

type TxLog struct {
	mu      sync.Mutex
	entries []string
}

func (d *DB) CommitWithLog(log *TxLog) {
	d.mu.Lock()
	log.mu.Lock() // want `potential deadlock: lock ordering cycle between DB\.mu and TxLog\.mu`
	log.entries = append(log.entries, d.data)
	log.mu.Unlock()
	d.mu.Unlock()
}

func (d *DB) FlushToDB(log *TxLog) {
	log.mu.Lock()
	d.mu.Lock()
	for _, e := range log.entries {
		d.data = e
	}
	d.mu.Unlock()
	log.mu.Unlock()
}

//mu:concurrent
func StartDB(d *DB, log *TxLog) {
	go d.CommitWithLog(log)
	go d.FlushToDB(log)
}

// --- Self-edge (same type, different instances) ---

type Account struct {
	mu      sync.Mutex
	balance int
}

func Transfer(from, to *Account) {
	from.mu.Lock()
	to.mu.Lock() // want `potential deadlock: lock ordering cycle on Account\.mu`
	from.balance -= 100
	to.balance += 100
	to.mu.Unlock()
	from.mu.Unlock()
}

//mu:concurrent
func StartTransfer(a, b *Account) {
	go Transfer(a, b)
	go Transfer(b, a)
}

// --- No cycle (consistent ordering): always Manager then Resource ---

type Manager struct {
	mu sync.Mutex
	id int
}

type Resource struct {
	mu   sync.Mutex
	name string
}

func (m *Manager) Acquire(r *Resource) {
	m.mu.Lock()
	r.mu.Lock()
	r.name = "acquired"
	_ = m.id
	r.mu.Unlock()
	m.mu.Unlock()
}

func (m *Manager) Release(r *Resource) {
	m.mu.Lock()
	r.mu.Lock()
	r.name = "released"
	_ = m.id
	r.mu.Unlock()
	m.mu.Unlock()
}

//mu:concurrent
func StartManager(m *Manager, r *Resource) {
	go m.Acquire(r)
	go m.Release(r)
}

// --- Interprocedural: A holds X, calls function that acquires Y; B holds Y, calls function that acquires X ---

type ServiceX struct {
	mu   sync.Mutex
	valX int
}

type ServiceY struct {
	mu   sync.Mutex
	valY int
}

func (y *ServiceY) DoWork() {
	y.mu.Lock()
	y.valY++
	y.mu.Unlock()
}

func (x *ServiceX) DoWork() {
	x.mu.Lock()
	x.valX++
	x.mu.Unlock()
}

func WithXThenY(x *ServiceX, y *ServiceY) {
	x.mu.Lock()
	y.DoWork() // want `potential deadlock: lock ordering cycle between ServiceX\.mu and ServiceY\.mu`
	x.mu.Unlock()
}

func WithYThenX(x *ServiceX, y *ServiceY) {
	y.mu.Lock()
	x.DoWork()
	y.mu.Unlock()
}

//mu:concurrent
func StartServices(x *ServiceX, y *ServiceY) {
	go WithXThenY(x, y)
	go WithYThenX(x, y)
}
