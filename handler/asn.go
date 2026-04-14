package handler

import "sort"

// ASNSummary is the aggregated view of an ASN derived from the route store.
type ASNSummary struct {
	ASN             uint32   `json:"asn"`
	PrefixCount     int      `json:"prefix_count"`
	Prefixes        []string `json:"prefixes"`
	Upstreams       []uint32 `json:"upstreams"`
	Tier1ASes       []uint32 `json:"tier1_ases,omitempty"`
	Downstreams     []uint32 `json:"downstreams"`
	AnnouncingPeers []uint32 `json:"announcing_peers"`
}

// GetASNSummary returns an aggregated summary for an ASN derived from the route store.
// Returns (nil, false) if the ASN has no originated prefixes in the store.
func (h *BGPHandler) GetASNSummary(asn uint32) (*ASNSummary, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	prefixSet := make(map[string]struct{})
	upstreamSet := make(map[uint32]struct{})
	tier1Set := make(map[uint32]struct{})
	downstreamSet := make(map[uint32]struct{})
	peerSet := make(map[uint32]struct{})

	for prefix, peerMap := range h.store {
		for _, a := range peerMap {
			if a.OriginAS == asn {
				prefixSet[prefix] = struct{}{}
				if a.PeerASN != 0 {
					peerSet[a.PeerASN] = struct{}{}
				}
				if a.DirectUpstream != 0 {
					upstreamSet[a.DirectUpstream] = struct{}{}
				}
				for _, u := range a.UpstreamChain {
					upstreamSet[u] = struct{}{}
				}
				if a.Tier1Found != 0 {
					tier1Set[a.Tier1Found] = struct{}{}
				}
			}
			// Collect downstreams: routes where asn is in the upstream chain
			for _, u := range a.UpstreamChain {
				if u == asn && a.OriginAS != asn {
					downstreamSet[a.OriginAS] = struct{}{}
				}
			}
		}
	}

	if len(prefixSet) == 0 {
		return nil, false
	}

	s := &ASNSummary{
		ASN:             asn,
		PrefixCount:     len(prefixSet),
		Prefixes:        make([]string, 0, len(prefixSet)),
		Upstreams:       make([]uint32, 0, len(upstreamSet)),
		Tier1ASes:       make([]uint32, 0, len(tier1Set)),
		Downstreams:     make([]uint32, 0, len(downstreamSet)),
		AnnouncingPeers: make([]uint32, 0, len(peerSet)),
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
		s.AnnouncingPeers = append(s.AnnouncingPeers, p)
	}
	sort.Strings(s.Prefixes)
	sort.Slice(s.Upstreams, func(i, j int) bool { return s.Upstreams[i] < s.Upstreams[j] })
	sort.Slice(s.Tier1ASes, func(i, j int) bool { return s.Tier1ASes[i] < s.Tier1ASes[j] })
	sort.Slice(s.Downstreams, func(i, j int) bool { return s.Downstreams[i] < s.Downstreams[j] })
	sort.Slice(s.AnnouncingPeers, func(i, j int) bool { return s.AnnouncingPeers[i] < s.AnnouncingPeers[j] })
	return s, true
}

// GetASNPrefixes returns all prefixes originated by the given ASN.
func (h *BGPHandler) GetASNPrefixes(asn uint32) []string {
	h.mu.RLock()
	defer h.mu.RUnlock()

	seen := make(map[string]struct{})
	for prefix, peerMap := range h.store {
		for _, a := range peerMap {
			if a.OriginAS == asn {
				seen[prefix] = struct{}{}
				break
			}
		}
	}
	out := make([]string, 0, len(seen))
	for p := range seen {
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
	for _, peerMap := range h.store {
		for _, a := range peerMap {
			if a.OriginAS != asn {
				continue
			}
			if a.DirectUpstream != 0 {
				set[a.DirectUpstream] = struct{}{}
			}
			for _, u := range a.UpstreamChain {
				set[u] = struct{}{}
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
	for _, peerMap := range h.store {
		for _, a := range peerMap {
			if a.OriginAS == asn {
				continue
			}
			for _, u := range a.UpstreamChain {
				if u == asn {
					set[a.OriginAS] = struct{}{}
					break
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

// GetASNPeers returns the ASNs of BGP peers announcing prefixes originated by the given ASN.
func (h *BGPHandler) GetASNPeers(asn uint32) []uint32 {
	h.mu.RLock()
	defer h.mu.RUnlock()

	set := make(map[uint32]struct{})
	for _, peerMap := range h.store {
		for _, a := range peerMap {
			if a.OriginAS == asn && a.PeerASN != 0 {
				set[a.PeerASN] = struct{}{}
			}
		}
	}
	out := make([]uint32, 0, len(set))
	for p := range set {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
