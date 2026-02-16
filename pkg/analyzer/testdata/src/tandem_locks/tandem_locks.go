package tandem_locks

import "sync"

// Service has two mutexes guarding different fields. Many read-heavy methods
// hold statusMu while also reading updatedAt, creating noise observations that
// outnumber the write observations under stateMu. The linter must still infer
// stateMu as the guard for updatedAt (because writes happen under stateMu).
//
// With the old frequency-based inference, statusMu would win (5 reads > 1
// write), producing wrong diagnostics. With write-priority inference, stateMu
// is correctly identified as the guard.
type Service struct {
	statusMu  sync.RWMutex
	status    string
	stateMu   sync.Mutex
	updatedAt int64
	count     int
}

// --- Methods that establish statusMu as the guard for status ---

func (s *Service) SetStatus(st string) {
	s.statusMu.Lock()
	s.status = st
	s.statusMu.Unlock()
}

func (s *Service) GetStatus() string {
	s.statusMu.RLock()
	defer s.statusMu.RUnlock()
	return s.status
}

// --- Methods that establish stateMu as the guard for updatedAt/count ---

func (s *Service) UpdateTimestamp(ts int64) {
	s.stateMu.Lock()
	s.updatedAt = ts
	s.stateMu.Unlock()
}

func (s *Service) Increment() {
	s.stateMu.Lock()
	s.count++
	s.stateMu.Unlock()
}

// --- Noise: read-heavy methods hold only statusMu.RLock while reading
// updatedAt. These create 5 observations where statusMu appears held for
// updatedAt — outnumbering the 1 write observation under stateMu. The linter
// correctly flags these as violations (accessing updatedAt without stateMu).
// The key assertion: the diagnostic says "stateMu" (correct guard), NOT
// "statusMu" (which the old algorithm would have inferred). ---

func (s *Service) StatusWithTimestamp1() (string, int64) {
	s.statusMu.RLock()
	defer s.statusMu.RUnlock()
	return s.status, s.updatedAt // want `field Service\.updatedAt is accessed without holding Service\.stateMu`
}

func (s *Service) StatusWithTimestamp2() (string, int64) {
	s.statusMu.RLock()
	defer s.statusMu.RUnlock()
	return s.status, s.updatedAt // want `field Service\.updatedAt is accessed without holding Service\.stateMu`
}

func (s *Service) StatusWithTimestamp3() (string, int64) {
	s.statusMu.RLock()
	defer s.statusMu.RUnlock()
	return s.status, s.updatedAt // want `field Service\.updatedAt is accessed without holding Service\.stateMu`
}

func (s *Service) StatusWithTimestamp4() (string, int64) {
	s.statusMu.RLock()
	defer s.statusMu.RUnlock()
	return s.status, s.updatedAt // want `field Service\.updatedAt is accessed without holding Service\.stateMu`
}

func (s *Service) StatusWithTimestamp5() (string, int64) {
	s.statusMu.RLock()
	defer s.statusMu.RUnlock()
	return s.status, s.updatedAt // want `field Service\.updatedAt is accessed without holding Service\.stateMu`
}

// --- Real violation: accessing updatedAt without any lock ---

func (s *Service) UnsafeRead() int64 {
	return s.updatedAt // want `field Service\.updatedAt is accessed without holding Service\.stateMu`
}
