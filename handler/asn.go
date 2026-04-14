package handler

import "sort"

// ASNSummary is the aggregated view of an ASN derived from the route store.
type ASNSummary struct {
	ASN         uint32   `json:"asn"`
	PrefixCount int      `json:"prefix_count"`
	Prefixes    []string `json:"prefixes"`
	Upstreams   []uint32 `json:"upstreams"`
	Tier1ASes   []uint32 `json:"tier1_ases,omitempty"`
	Downstreams []uint32 `json:"downstreams"`
	Peers       []uint32 `json:"peers"`
}

// containsASN reports whether asn appears anywhere in slice.
func containsASN(slice []uint32, asn uint32) bool {
	for _, v := range slice {
		if v == asn {
			return true
		}
	}
	return false
}

// GetASNSummary returns an aggregated summary for an ASN derived from the route store.
// Returns (nil, false) if the ASN has no originated prefixes in the store.
func (h *BGPHandler) GetASNSummary(asn uint32) (*ASNSummary, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	// Prefixes, upstreams, tier1s — scan only the prefixes this ASN originates.
	prefixSet := make(map[string]struct{})
	upstreamSet := make(map[uint32]struct{})
	tier1Set := make(map[uint32]struct{})

	for prefix := range h.originIndex[asn] {
		peerMap, ok := h.store[prefix]
		if !ok {
			continue
		}
		for _, a := range peerMap {
			if a.OriginAS != asn {
				continue
			}
			prefixSet[prefix] = struct{}{}
			if a.DirectUpstream != 0 {
				upstreamSet[a.DirectUpstream] = struct{}{}
			}
			for _, u := range a.UpstreamChain {
				if u != 0 {
					upstreamSet[u] = struct{}{}
				}
			}
			if a.Tier1Found != 0 {
				tier1Set[a.Tier1Found] = struct{}{}
			}
		}
	}

	if len(prefixSet) == 0 {
		return nil, false
	}

	// Downstreams — use transitIndex as candidate list, verify each via originIndex.
	downstreamSet := make(map[uint32]struct{})
	for candidateOrigin := range h.transitIndex[asn] {
		if candidateOrigin == asn {
			continue
		}
	downstreamPrefixLoop:
		for prefix := range h.originIndex[candidateOrigin] {
			peerMap, ok := h.store[prefix]
			if !ok {
				continue
			}
			for _, a := range peerMap {
				if a.OriginAS != candidateOrigin {
					continue
				}
				if containsASN(a.UpstreamChain, asn) {
					downstreamSet[candidateOrigin] = struct{}{}
					break downstreamPrefixLoop
				}
			}
		}
	}

	// Peers — adjacent ASNs (from adjacentIndex) minus upstreams, downstreams, self.
	peerSet := make(map[uint32]struct{})
	for adj := range h.adjacentIndex[asn] {
		if adj == asn {
			continue
		}
		if _, ok := upstreamSet[adj]; ok {
			continue
		}
		if _, ok := downstreamSet[adj]; ok {
			continue
		}
		peerSet[adj] = struct{}{}
	}

	s := &ASNSummary{
		ASN:         asn,
		PrefixCount: len(prefixSet),
		Prefixes:    make([]string, 0, len(prefixSet)),
		Upstreams:   make([]uint32, 0, len(upstreamSet)),
		Tier1ASes:   make([]uint32, 0, len(tier1Set)),
		Downstreams: make([]uint32, 0, len(downstreamSet)),
		Peers:       make([]uint32, 0, len(peerSet)),
	}
	for p := range prefixSet {
		s.Prefixes = append(s.Prefixes, p)
	}
	for u := range upstreamSet {
		s.Upstreams = append(s.Upstreams, u)
	}
	for t := range tier1Set {
		s.Tier1ASes = append(s.Tier1ASes, t)
	}
	for d := range downstreamSet {
		s.Downstreams = append(s.Downstreams, d)
	}
	for p := range peerSet {
		s.Peers = append(s.Peers, p)
	}
	sort.Strings(s.Prefixes)
	sort.Slice(s.Upstreams, func(i, j int) bool { return s.Upstreams[i] < s.Upstreams[j] })
	sort.Slice(s.Tier1ASes, func(i, j int) bool { return s.Tier1ASes[i] < s.Tier1ASes[j] })
	sort.Slice(s.Downstreams, func(i, j int) bool { return s.Downstreams[i] < s.Downstreams[j] })
	sort.Slice(s.Peers, func(i, j int) bool { return s.Peers[i] < s.Peers[j] })
	return s, true
}

// GetASNPrefixes returns all prefixes originated by the given ASN.
func (h *BGPHandler) GetASNPrefixes(asn uint32) []string {
	h.mu.RLock()
	defer h.mu.RUnlock()

	out := make([]string, 0, len(h.originIndex[asn]))
	for p := range h.originIndex[asn] {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// GetASNUpstreams returns the unique upstream ASNs for the given ASN.
func (h *BGPHandler) GetASNUpstreams(asn uint32) []uint32 {
	h.mu.RLock()
	defer h.mu.RUnlock()

	set := make(map[uint32]struct{})
	for prefix := range h.originIndex[asn] {
		peerMap, ok := h.store[prefix]
		if !ok {
			continue
		}
		for _, a := range peerMap {
			if a.OriginAS != asn {
				continue
			}
			if a.DirectUpstream != 0 {
				set[a.DirectUpstream] = struct{}{}
			}
			for _, u := range a.UpstreamChain {
				if u != 0 {
					set[u] = struct{}{}
				}
			}
		}
	}
	out := make([]uint32, 0, len(set))
	for u := range set {
		out = append(out, u)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// GetASNDownstreams returns ASNs that use the given ASN as a transit provider.
func (h *BGPHandler) GetASNDownstreams(asn uint32) []uint32 {
	h.mu.RLock()
	defer h.mu.RUnlock()

	set := make(map[uint32]struct{})
	for candidateOrigin := range h.transitIndex[asn] {
		if candidateOrigin == asn {
			continue
		}
	prefixLoop:
		for prefix := range h.originIndex[candidateOrigin] {
			peerMap, ok := h.store[prefix]
			if !ok {
				continue
			}
			for _, a := range peerMap {
				if a.OriginAS != candidateOrigin {
					continue
				}
				if containsASN(a.UpstreamChain, asn) {
					set[candidateOrigin] = struct{}{}
					break prefixLoop
				}
			}
		}
	}
	out := make([]uint32, 0, len(set))
	for d := range set {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// GetASNPeers returns ASNs that are BGP peers of the given ASN — adjacent in collected
// AS paths but not in a transit relationship (neither upstream nor downstream).
func (h *BGPHandler) GetASNPeers(asn uint32) []uint32 {
	h.mu.RLock()
	defer h.mu.RUnlock()

	// Upstreams: scan only the prefixes this ASN originates.
	upstreamSet := make(map[uint32]struct{})
	for prefix := range h.originIndex[asn] {
		peerMap, ok := h.store[prefix]
		if !ok {
			continue
		}
		for _, a := range peerMap {
			if a.OriginAS != asn {
				continue
			}
			if a.DirectUpstream != 0 {
				upstreamSet[a.DirectUpstream] = struct{}{}
			}
			for _, u := range a.UpstreamChain {
				if u != 0 {
					upstreamSet[u] = struct{}{}
				}
			}
		}
	}

	// Downstreams: transitIndex candidates, verified via originIndex.
	downstreamSet := make(map[uint32]struct{})
	for candidateOrigin := range h.transitIndex[asn] {
		if candidateOrigin == asn {
			continue
		}
	peerPrefixLoop:
		for prefix := range h.originIndex[candidateOrigin] {
			peerMap, ok := h.store[prefix]
			if !ok {
				continue
			}
			for _, a := range peerMap {
				if a.OriginAS != candidateOrigin {
					continue
				}
				if containsASN(a.UpstreamChain, asn) {
					downstreamSet[candidateOrigin] = struct{}{}
					break peerPrefixLoop
				}
			}
		}
	}

	// Peers: adjacentIndex[asn] minus upstreams, downstreams, self.
	out := make([]uint32, 0)
	for adj := range h.adjacentIndex[asn] {
		if adj == asn {
			continue
		}
		if _, ok := upstreamSet[adj]; ok {
			continue
		}
		if _, ok := downstreamSet[adj]; ok {
			continue
		}
		out = append(out, adj)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
