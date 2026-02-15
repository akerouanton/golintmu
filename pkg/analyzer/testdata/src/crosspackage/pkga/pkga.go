package pkga

import "sync"

type Stats struct { // want Stats:`FieldGuardFact\{1->0 2->0\}`
	mu           sync.Mutex
	RequestCount int // want `field Stats\.RequestCount is guarded by Stats\.mu but is exported`
	errorCount   int
}

func (s *Stats) Inc() { // want Inc:`FuncLockFact\{requires=\[\] acquires=\[Stats\.0\]\}`
	s.mu.Lock()
	defer s.mu.Unlock()
	s.RequestCount++
	s.errorCount++
}

func (s *Stats) LockedGet() int { // want LockedGet:`FuncLockFact\{requires=\[\] acquires=\[Stats\.0\]\}`
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.errorCount
}

// UnlockedHelper accesses errorCount without the lock. This establishes a
// requirement that callers must hold Stats.mu.
func (s *Stats) UnlockedHelper() int { // want UnlockedHelper:`FuncLockFact\{requires=\[Stats\.0\] acquires=\[\]\}`
	return s.errorCount // want `field Stats\.errorCount is accessed without holding Stats\.mu`
}
