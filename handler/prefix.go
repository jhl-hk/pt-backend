package handler

import (
	"net"
	"sort"
	"time"

	"github.com/jianyuelab/pt-backend/bgp"
)

// PrefixSummary is the aggregated view of a prefix across all announcing peers.
type PrefixSummary struct {
	Prefix      string   `json:"prefix"`
	PeerCount   int      `json:"peer_count"`
	OriginASes  []uint32 `json:"origin_ases"`
	Upstreams   []uint32 `json:"upstreams"`
	Tier1ASes   []uint32 `json:"tier1_ases,omitempty"`
	LastUpdated string   `json:"last_updated"`
}

// GetAll returns a summary for every prefix currently in the store.
func (h *BGPHandler) GetAll() []*PrefixSummary {
	h.mu.RLock()
	defer h.mu.RUnlock()

	out := make([]*PrefixSummary, 0, len(h.store))
	for prefix, peerMap := range h.store {
		out = append(out, summarize(prefix, peerMap))
	}
	return out
}

// GetPrefixSummary returns the aggregated summary for a single prefix.
func (h *BGPHandler) GetPrefixSummary(prefix string) (*PrefixSummary, bool) {
	h.mu.RLock()
	peerMap, ok := h.store[prefix]
	h.mu.RUnlock()
	if !ok {
		return nil, false
	}
	return summarize(prefix, peerMap), true
}

// GetPrefixRoutes returns all stored routes for a prefix, one per announcing peer.
func (h *BGPHandler) GetPrefixRoutes(prefix string) []*bgp.PrefixAnalysis {
	h.mu.RLock()
	peerMap, ok := h.store[prefix]
	h.mu.RUnlock()

	routes := make([]*bgp.PrefixAnalysis, 0)
	if !ok {
		return routes
	}
	for _, a := range peerMap {
		routes = append(routes, a)
	}
	sort.Slice(routes, func(i, j int) bool {
		return routes[i].Neighbor < routes[j].Neighbor
	})
	return routes
}

// GetPrefixPeers returns the ASNs of all BGP peers announcing the given prefix.
func (h *BGPHandler) GetPrefixPeers(prefix string) []uint32 {
	h.mu.RLock()
	peerMap, ok := h.store[prefix]
	h.mu.RUnlock()

	set := make(map[uint32]struct{})
	if ok {
		for _, a := range peerMap {
			if a.PeerASN != 0 {
				set[a.PeerASN] = struct{}{}
			}
		}
	}
	out := make([]uint32, 0, len(set))
	for asn := range set {
		out = append(out, asn)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// GetSubPrefixes returns all more-specific (downstream) prefixes contained within prefix.
func (h *BGPHandler) GetSubPrefixes(prefix string) []string {
	subs := make([]string, 0)
	_, parent, err := net.ParseCIDR(prefix)
	if err != nil {
		return subs
	}
	parentBits, _ := parent.Mask.Size()

	h.mu.RLock()
	defer h.mu.RUnlock()

	for p := range h.store {
		if p == prefix {
			continue
		}
		_, child, err := net.ParseCIDR(p)
		if err != nil {
			continue
		}
		childBits, _ := child.Mask.Size()
		if parent.Contains(child.IP) && childBits > parentBits {
			subs = append(subs, p)
		}
	}
	sort.Strings(subs)
	return subs
}

func summarize(prefix string, peerMap map[string]*bgp.PrefixAnalysis) *PrefixSummary {
	originSet := make(map[uint32]struct{})
	upstreamSet := make(map[uint32]struct{})
	tier1Set := make(map[uint32]struct{})
	var lastUpdated time.Time

	for _, a := range peerMap {
		if a.OriginAS != 0 {
			originSet[a.OriginAS] = struct{}{}
		}
		for _, u := range a.UpstreamChain {
			upstreamSet[u] = struct{}{}
		}
		if a.Tier1Found != 0 {
			tier1Set[a.Tier1Found] = struct{}{}
		}
		if a.ReceivedAt.After(lastUpdated) {
			lastUpdated = a.ReceivedAt
		}
	}

	s := &PrefixSummary{
		Prefix:    prefix,
		PeerCount: len(peerMap),
	}
	if !lastUpdated.IsZero() {
		s.LastUpdated = lastUpdated.UTC().Format(time.RFC3339)
	}
	for asn := range originSet {
		s.OriginASes = append(s.OriginASes, asn)
	}
	for asn := range upstreamSet {
		s.Upstreams = append(s.Upstreams, asn)
	}
	for asn := range tier1Set {
		s.Tier1ASes = append(s.Tier1ASes, asn)
	}
	sort.Slice(s.OriginASes, func(i, j int) bool { return s.OriginASes[i] < s.OriginASes[j] })
	sort.Slice(s.Upstreams, func(i, j int) bool { return s.Upstreams[i] < s.Upstreams[j] })
	sort.Slice(s.Tier1ASes, func(i, j int) bool { return s.Tier1ASes[i] < s.Tier1ASes[j] })
	return s
}
