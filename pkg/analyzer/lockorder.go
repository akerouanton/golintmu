package analyzer

import (
	"go/token"
	"sort"

	"golang.org/x/tools/go/ssa"
)

// lockOrderEdge records that lockTo was acquired while lockFrom was held.
type lockOrderEdge struct {
	From mutexFieldKey
	To   mutexFieldKey
	Pos  token.Pos     // where the second lock was acquired
	Fn   *ssa.Function // function containing the acquisition
}

// lockOrderCycle is a sequence of edges forming a cycle in the lock-order graph.
type lockOrderCycle []lockOrderEdge

// lockOrderGraph is a directed graph of lock acquisition order.
// An edge From→To means "To was acquired while From was held."
type lockOrderGraph struct {
	edges map[mutexFieldKey][]lockOrderEdge
}

func newLockOrderGraph() *lockOrderGraph {
	return &lockOrderGraph{
		edges: make(map[mutexFieldKey][]lockOrderEdge),
	}
}

// addEdge adds an edge to the graph, deduplicating by (From, To, Fn).
func (g *lockOrderGraph) addEdge(edge lockOrderEdge) {
	for _, existing := range g.edges[edge.From] {
		if existing.To == edge.To && existing.Fn == edge.Fn {
			return // already recorded
		}
	}
	g.edges[edge.From] = append(g.edges[edge.From], edge)
}

// detectCycles finds all cycles in the lock-order graph using DFS with
// white/gray/black coloring.
func (g *lockOrderGraph) detectCycles() []lockOrderCycle {
	const (
		white = iota // unvisited
		gray         // in current DFS path
		black        // fully processed
	)

	color := make(map[mutexFieldKey]int)
	parent := make(map[mutexFieldKey]lockOrderEdge) // edge that led to this node

	var cycles []lockOrderCycle

	var dfs func(node mutexFieldKey)
	dfs = func(node mutexFieldKey) {
		color[node] = gray
		// Sort edges for deterministic traversal.
		edges := g.edges[node]
		sort.Slice(edges, func(i, j int) bool {
			ni := mutexFieldKeyName(edges[i].To)
			nj := mutexFieldKeyName(edges[j].To)
			return ni < nj
		})
		for _, edge := range edges {
			switch color[edge.To] {
			case white:
				parent[edge.To] = edge
				dfs(edge.To)
			case gray:
				// Back-edge found — extract cycle.
				cycle := extractCycle(parent, edge)
				if cycle != nil {
					cycles = append(cycles, cycle)
				}
			}
		}
		color[node] = black
	}

	// Collect all nodes (both sources and targets of edges).
	nodes := make(map[mutexFieldKey]bool)
	for from, edges := range g.edges {
		nodes[from] = true
		for _, e := range edges {
			nodes[e.To] = true
		}
	}

	// Sort nodes for deterministic DFS order.
	sortedNodes := make([]mutexFieldKey, 0, len(nodes))
	for node := range nodes {
		sortedNodes = append(sortedNodes, node)
	}
	sort.Slice(sortedNodes, func(i, j int) bool {
		ni := mutexFieldKeyName(sortedNodes[i])
		nj := mutexFieldKeyName(sortedNodes[j])
		return ni < nj
	})

	for _, node := range sortedNodes {
		if color[node] == white {
			dfs(node)
		}
	}

	return deduplicateCycles(cycles)
}

// extractCycle traces the parent map from the back-edge target back to the source
// to build the cycle. The back-edge is: edge.From (gray) → edge.To (gray).
func extractCycle(parent map[mutexFieldKey]lockOrderEdge, backEdge lockOrderEdge) lockOrderCycle {
	var cycle lockOrderCycle
	cycle = append(cycle, backEdge)

	// Walk backwards from backEdge.From to backEdge.To through parent edges.
	// Safety: visited set prevents infinite loops if parent map is malformed.
	current := backEdge.From
	visited := make(map[mutexFieldKey]bool)
	for current != backEdge.To {
		if visited[current] {
			return nil // cycle in parent chain — shouldn't happen
		}
		visited[current] = true
		edge, ok := parent[current]
		if !ok {
			return nil // shouldn't happen in a valid DFS
		}
		cycle = append(cycle, edge)
		current = edge.From
	}

	// Reverse to get edges in acquisition order.
	for i, j := 0, len(cycle)-1; i < j; i, j = i+1, j-1 {
		cycle[i], cycle[j] = cycle[j], cycle[i]
	}

	return cycle
}

// deduplicateCycles removes cycles that represent the same set of edges
// (same cycle discovered from different starting nodes).
func deduplicateCycles(cycles []lockOrderCycle) []lockOrderCycle {
	type edgePair struct {
		from, to mutexFieldKey
	}

	seen := make(map[string]bool)
	var result []lockOrderCycle

	for _, cycle := range cycles {
		// Build a canonical key from sorted edge pairs.
		pairs := make([]edgePair, len(cycle))
		for i, e := range cycle {
			pairs[i] = edgePair{from: e.From, to: e.To}
		}

		// Find the lexicographically smallest rotation.
		minIdx := 0
		for i := 1; i < len(pairs); i++ {
			if pairLess(pairs[i], pairs[minIdx]) {
				minIdx = i
			}
		}

		// Build key from the canonical rotation.
		key := ""
		for i := 0; i < len(pairs); i++ {
			p := pairs[(minIdx+i)%len(pairs)]
			key += mutexFieldKeyName(p.from) + "->" + mutexFieldKeyName(p.to) + ";"
		}

		if !seen[key] {
			seen[key] = true
			result = append(result, cycle)
		}
	}

	return result
}

func pairLess(a, b struct{ from, to mutexFieldKey }) bool {
	aName := mutexFieldKeyName(a.from) + "->" + mutexFieldKeyName(a.to)
	bName := mutexFieldKeyName(b.from) + "->" + mutexFieldKeyName(b.to)
	return aName < bName
}
