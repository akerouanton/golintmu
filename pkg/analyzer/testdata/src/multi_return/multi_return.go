package multi_return

import (
	"errors"
	"sync"
)

// Reproduction test: Lock/Unlock pair after multiple early-return branches.
// These should NOT produce "Unlock() called but X is not held" false positives.

type Driver struct {
	mu       sync.Mutex
	networks map[string]string
}

func NewDriver() *Driver {
	return &Driver{networks: make(map[string]string)}
}

func step() error { return nil }

func cond() bool { return true }

// CreateNetwork has multiple if-err-return branches before a Lock/Unlock pair.
func (d *Driver) CreateNetwork(id string) (string, error) {
	if err := step(); err != nil {
		return "", err
	}
	if err := step(); err != nil {
		return "", err
	}

	d.mu.Lock()
	d.networks[id] = id
	d.mu.Unlock()

	return id, nil
}

// --- Network struct ---

type Network struct {
	stateMu   sync.Mutex
	endpoints map[string]string
	updatedAt int64
}

// ConnectEndpoint has conditional blocks (nested if) and Lock/Unlock at the end.
func (nw *Network) ConnectEndpoint(id string, skipAlloc bool) (string, error) {
	if !skipAlloc {
		if err := step(); err != nil {
			return "", err
		}
	}

	nw.stateMu.Lock()
	nw.endpoints[id] = id
	nw.updatedAt = 42
	nw.stateMu.Unlock()

	return id, nil
}

// --- With named returns and defer ---

type Server struct {
	mu    sync.Mutex
	conns map[string]string
}

func (s *Server) Connect(id string) (_ string, retErr error) {
	defer func() {
		if retErr != nil {
			// cleanup
		}
	}()

	if err := step(); err != nil {
		return "", err
	}

	s.mu.Lock()
	s.conns[id] = id
	s.mu.Unlock()

	return id, nil
}

// --- KEY TEST: Closure capturing the receiver (lifted variable) ---
// In Go SSA, when a closure captures a variable, the variable is "lifted"
// to a heap allocation. Each use becomes a load from the heap cell, creating
// DIFFERENT SSA values for what is logically the same variable.
// This must NOT cause false positives.

type Manager struct {
	mu    sync.Mutex
	items map[string]string
}

// AddWithCapture: the defer closure captures m (the receiver), which causes
// the SSA builder to lift m to a heap allocation. The Lock and Unlock calls
// then use different SSA load instructions for m, but they refer to the same
// logical variable.
func (m *Manager) AddWithCapture(id string) (_ string, retErr error) {
	// This defer captures m and retErr, causing m to be lifted.
	defer func() {
		if retErr != nil {
			_ = m.items // reference m in closure to force lifting
		}
	}()

	if err := step(); err != nil {
		return "", err
	}

	if id == "" {
		return "", errors.New("empty id")
	}

	m.mu.Lock()
	m.items[id] = id
	m.mu.Unlock()

	return id, nil
}

// ConditionalDeferCapture: conditional defers that capture the receiver.
func (m *Manager) ConditionalDeferCapture(id string, allocA, allocB bool) (_ string, retErr error) {
	if allocA {
		if err := step(); err != nil {
			return "", err
		}
		defer func() {
			if retErr != nil {
				_ = m.items // capture m
			}
		}()
	}

	if allocB {
		if err := step(); err != nil {
			return "", err
		}
		defer func() {
			if retErr != nil {
				_ = m.items // capture m
			}
		}()
	}

	if cond() || cond() {
		if err := step(); err != nil {
			return "", err
		}
	}

	m.mu.Lock()
	m.items[id] = id
	m.mu.Unlock()

	return id, nil
}

// --- Establish guard inference for all structs above ---

func (d *Driver) GetNetwork(id string) string {
	d.mu.Lock()
	v := d.networks[id]
	d.mu.Unlock()
	return v
}

func (nw *Network) GetEndpoint(id string) string {
	nw.stateMu.Lock()
	v := nw.endpoints[id]
	nw.stateMu.Unlock()
	return v
}

func (s *Server) GetConn(id string) string {
	s.mu.Lock()
	v := s.conns[id]
	s.mu.Unlock()
	return v
}

func (m *Manager) GetItem(id string) string {
	m.mu.Lock()
	v := m.items[id]
	m.mu.Unlock()
	return v
}
