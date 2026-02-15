package concurrent

import (
	"net/http"
	"sync"
)

// --- Struct used across tests ---

type Server struct {
	mu      sync.Mutex
	clients int
}

// lockedAdd always acquires the lock — no violation.
func (s *Server) lockedAdd() {
	s.mu.Lock()
	s.clients++
	s.mu.Unlock()
}

// unsafeRead accesses without lock — reported when called from concurrent context.
func (s *Server) unsafeRead() int {
	return s.clients // want `field Server\.clients is accessed without holding Server\.mu`
}

// --- go statement: direct method call ---

func launchDirect(s *Server) {
	go s.unsafeRead() // launched via go — unsafeRead is concurrent entrypoint
}

// --- go statement: closure ---

func launchClosure(s *Server) {
	go func() {
		_ = s.clients // want `field Server\.clients is accessed without holding Server\.mu`
	}()
}

// --- go statement: named function ---

func goTarget(s *Server) {
	_ = s.clients // want `field Server\.clients is accessed without holding Server\.mu`
}

func launchNamed(s *Server) {
	go goTarget(s)
}

// --- Non-concurrent function: no go, not reachable from go/handler ---

func setupCode(s *Server) {
	s.clients = 42 // no diagnostic: not concurrent
}

// --- ServeHTTP method: concurrent entrypoint ---

type Handler struct {
	mu    sync.Mutex
	count int
}

func (h *Handler) lockedInc() {
	h.mu.Lock()
	h.count++
	h.mu.Unlock()
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	_ = h.count // want `field Handler\.count is accessed without holding Handler\.mu`
}

// --- http.HandleFunc: concurrent entrypoint ---

type Registry struct {
	mu    sync.Mutex
	items int
}

func (reg *Registry) lockedSet() {
	reg.mu.Lock()
	reg.items = 1
	reg.mu.Unlock()
}

func SetupRoutes(reg *Registry) {
	http.HandleFunc("/items", func(w http.ResponseWriter, r *http.Request) {
		_ = reg.items // want `field Registry\.items is accessed without holding Registry\.mu`
	})
}

// --- Non-concurrent init function: should NOT report ---

func initServer(s *Server) {
	s.clients = 0 // no diagnostic: not concurrent
}
