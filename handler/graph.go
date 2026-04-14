package handler

import "sort"

// GraphNode represents one ASN in a path graph.
type GraphNode struct {
	ASN  uint32 `json:"asn"`
	Role string `json:"role"` // "origin", "tier1", "upstream", "peer_collector"
}

// GraphEdge represents a direct BGP adjacency between two ASNs.
type GraphEdge struct {
	Source uint32 `json:"source"`
	Target uint32 `json:"target"`
	Count  int    `json:"count"` // number of collected paths that contain this adjacency
}

// GraphData is the full graph payload returned by the /graph endpoints.
type GraphData struct {
	Nodes []GraphNode `json:"nodes"`
	Edges []GraphEdge `json:"edges"`
	Paths [][]uint32  `json:"paths"` // one raw AS path per announcing peer
}

// GetPrefixGraph returns a graph of all AS paths collected for a prefix.
// Returns (nil, false) if the prefix is not in the store.
func (h *BGPHandler) GetPrefixGraph(prefix string) (*GraphData, bool) {
	h.mu.RLock()
	peerMap, ok := h.store[prefix]
	if !ok {
		h.mu.RUnlock()
		return nil, false
	}

	// nodeRole: assign the most specific role (origin > tier1 > upstream > peer_collector).
	// Role priority: origin=4, tier1=3, upstream=2, peer_collector=1.
	nodeRole := make(map[uint32]int) // asn → priority
	edgeCount := make(map[[2]uint32]int)
	var paths [][]uint32

	for _, a := range peerMap {
		if len(a.ASPath) == 0 {
			continue
		}
		// Copy the AS path.
		path := make([]uint32, len(a.ASPath))
		copy(path, a.ASPath)
		paths = append(paths, path)

		// Assign roles.
		setRole := func(asn uint32, priority int) {
			if nodeRole[asn] < priority {
				nodeRole[asn] = priority
			}
		}
		if a.OriginAS != 0 {
			setRole(a.OriginAS, 4)
		}
		if a.Tier1Found != 0 {
			setRole(a.Tier1Found, 3)
		}
		for _, u := range a.UpstreamChain {
			if u != 0 {
				setRole(u, 2)
			}
		}
		if a.PeerASN != 0 {
			setRole(a.PeerASN, 1)
		}

		// Count edges.
		for i := 0; i < len(a.ASPath)-1; i++ {
			src, dst := a.ASPath[i], a.ASPath[i+1]
			if src == 0 || dst == 0 {
				continue
			}
			edgeCount[[2]uint32{src, dst}]++
		}
	}
	h.mu.RUnlock()

	// Build output.
	priorityToRole := map[int]string{4: "origin", 3: "tier1", 2: "upstream", 1: "peer_collector"}
	nodes := make([]GraphNode, 0, len(nodeRole))
	for asn, pri := range nodeRole {
		nodes = append(nodes, GraphNode{ASN: asn, Role: priorityToRole[pri]})
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].ASN < nodes[j].ASN })

	edges := make([]GraphEdge, 0, len(edgeCount))
	for pair, cnt := range edgeCount {
		edges = append(edges, GraphEdge{Source: pair[0], Target: pair[1], Count: cnt})
	}
	sort.Slice(edges, func(i, j int) bool {
		if edges[i].Source != edges[j].Source {
			return edges[i].Source < edges[j].Source
		}
		return edges[i].Target < edges[j].Target
	})

	sort.Slice(paths, func(i, j int) bool {
		if len(paths[i]) != len(paths[j]) {
			return len(paths[i]) < len(paths[j])
		}
		for k := range paths[i] {
			if paths[i][k] != paths[j][k] {
				return paths[i][k] < paths[j][k]
			}
		}
		return false
	})

	return &GraphData{Nodes: nodes, Edges: edges, Paths: paths}, true
}

// GetASNGraph returns a relationship graph centred on the given ASN:
// the ASN itself, its upstreams, its direct peers, and a sample of its downstreams.
func (h *BGPHandler) GetASNGraph(asn uint32) *GraphData {
	h.mu.RLock()
	defer h.mu.RUnlock()

	nodeRole := make(map[uint32]string)
	nodeRole[asn] = "center"

	edgeCount := make(map[[2]uint32]int)

	// Upstreams — from originated prefixes.
	for prefix := range h.originIndex[asn] {
		peerMap, ok := h.store[prefix]
		if !ok {
			continue
		}
		for _, a := range peerMap {
			if a.OriginAS != asn {
				continue
			}
			for _, u := range a.UpstreamChain {
				if u == 0 {
					continue
				}
				if _, exists := nodeRole[u]; !exists {
					nodeRole[u] = "upstream"
				}
				edgeCount[[2]uint32{asn, u}]++
			}
		}
	}

	// Downstreams — via transitIndex (eventually consistent, verified implicitly by presence).
	for candidateOrigin := range h.transitIndex[asn] {
		if candidateOrigin == asn {
			continue
		}
		if _, exists := nodeRole[candidateOrigin]; !exists {
			nodeRole[candidateOrigin] = "downstream"
		}
		edgeCount[[2]uint32{candidateOrigin, asn}]++
	}

	// Peers — from adjacentIndex, excluding upstreams, downstreams, self.
	for adj := range h.adjacentIndex[asn] {
		if adj == asn {
			continue
		}
		if role, exists := nodeRole[adj]; exists && role != "center" {
			continue // already classified as upstream or downstream
		}
		nodeRole[adj] = "peer"
		edgeCount[[2]uint32{asn, adj}]++
	}

	nodes := make([]GraphNode, 0, len(nodeRole))
	for a, role := range nodeRole {
		nodes = append(nodes, GraphNode{ASN: a, Role: role})
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].ASN < nodes[j].ASN })

	edges := make([]GraphEdge, 0, len(edgeCount))
	for pair, cnt := range edgeCount {
		edges = append(edges, GraphEdge{Source: pair[0], Target: pair[1], Count: cnt})
	}
	sort.Slice(edges, func(i, j int) bool {
		if edges[i].Source != edges[j].Source {
			return edges[i].Source < edges[j].Source
		}
		return edges[i].Target < edges[j].Target
	})

	return &GraphData{Nodes: nodes, Edges: edges, Paths: nil}
}
