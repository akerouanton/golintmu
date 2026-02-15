package pkgb

import "crosspackage/pkga"

// directAccess reads an exported guarded field without holding the lock.
// Tests FieldGuardFact import.
func directAccess(s *pkga.Stats) int {
	return s.RequestCount // want `field Stats\.RequestCount is accessed without holding Stats\.mu`
}

// callWithoutLock calls an imported function that requires the lock,
// without holding it. Tests FuncLockFact import.
func callWithoutLock(s *pkga.Stats) int {
	return s.UnlockedHelper() // want `Stats\.mu must be held when calling UnlockedHelper\(\)`
}

// callLockedInternally calls an imported function that handles locking
// internally. No violation expected.
func callLockedInternally(s *pkga.Stats) {
	s.Inc()
}
