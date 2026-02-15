package concurrent

import "sync"

// --- Reachability: functions reachable from go targets via call chains ---

type Cache struct {
	mu   sync.Mutex
	data int
}

func (c *Cache) lockedWrite() {
	c.mu.Lock()
	c.data = 1
	c.mu.Unlock()
}

// level2 accesses data without lock — has a caller so violation propagates up.
func (c *Cache) level2() int {
	return c.data
}

// level1 calls level2 without lock — has a caller so violation propagates up.
func (c *Cache) level1() int {
	return c.level2()
}

// goEntry is launched via go — concurrent entrypoint with no callers.
// The interprocedural violation surfaces here.
func (c *Cache) goEntry() {
	c.level1() // want `Cache\.mu must be held when calling level1\(\)`
}

func startCache(c *Cache) {
	go c.goEntry()
}

// --- unreachableHelper is NOT called from any go target ---

func (c *Cache) unreachableHelper() int {
	return c.data // no diagnostic: not reachable from concurrent entrypoint
}

func setupCache(c *Cache) {
	c.unreachableHelper() // no diagnostic: not concurrent
}
