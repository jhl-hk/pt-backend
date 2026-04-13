package handler

import "sort"

// Relationships holds the computed peer/upstream/downstream sets for an ASN.
type Relationships struct {
	UpstreamCount   int   `json:"upstream_count"`
	DownstreamCount int   `json:"downstream_count"`
	PeerCount       int   `json:"peer_count"`
	ConeSize        int   `json:"cone_size"`
	Upstream        []int `json:"upstream"`
	Downstream      []int `json:"downstream"`
	Peers           []int `json:"peers"`
	Cone            []int `json:"cone"`
}

// CalculateRelationships derives upstream, downstream, and peer ASNs for the
// given ASN from the routing index.
//
// Definitions (all relative to the queried ASN X):
//   - upstream:   ASNs that appear immediately before X in X's own prefix paths
//     (X's transit providers). Only 0 (unknown transit) is excluded.
//   - downstream: ASNs Y (≠ X) whose prefix paths contain X as an intermediate
//     (X appears before Y's origin).
//   - peers:      ASNs adjacent to X in any path that are neither upstream nor
//     downstream of X. Only 0 is excluded.
func CalculateRelationships(asn int, index map[int][]PrefixPath) Relationships {
	upstreamSet := make(map[int]bool)
	downstreamSet := make(map[int]bool)

	// Scan every path in the index for X's position.
	for originASN, prefixes := range index {
		for _, pp := range prefixes {
			for i, a := range pp.Path {
				if a != asn {
					continue
				}
				// Predecessor of X in any path → upstream of X.
				if i > 0 {
					prev := pp.Path[i-1]
					if prev != 0 {
						upstreamSet[prev] = true
					}
				}
				// X as intermediate → originASN is downstream.
				if originASN != asn && i < len(pp.Path)-1 {
					downstreamSet[originASN] = true
				}
			}
		}
	}

	// Peers: superset of upstream + downstream + immediate neighbours.
	peerSet := make(map[int]bool)
	for a := range upstreamSet {
		peerSet[a] = true
	}
	for a := range downstreamSet {
		peerSet[a] = true
	}
	for _, prefixes := range index {
		for _, pp := range prefixes {
			for i, a := range pp.Path {
				if a != asn {
					continue
				}
				if i > 0 && pp.Path[i-1] != 0 {
					peerSet[pp.Path[i-1]] = true
				}
				if i < len(pp.Path)-1 && pp.Path[i+1] != 0 {
					peerSet[pp.Path[i+1]] = true
				}
			}
		}
	}

	upstream := sortedKeys(upstreamSet)
	downstream := sortedKeys(downstreamSet)
	peers := sortedKeys(peerSet)

	// Cone: X itself plus all ASNs that route through X (transitive downstreams
	// are already captured by the routing table).
	coneSet := make(map[int]bool, len(downstreamSet)+1)
	coneSet[asn] = true
	for a := range downstreamSet {
		coneSet[a] = true
	}
	cone := sortedKeys(coneSet)

	return Relationships{
		UpstreamCount:   len(upstream),
		DownstreamCount: len(downstream),
		PeerCount:       len(peers),
		ConeSize:        len(cone),
		Upstream:        upstream,
		Downstream:      downstream,
		Peers:           peers,
		Cone:            cone,
	}
}

func sortedKeys(m map[int]bool) []int {
	keys := make([]int, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Ints(keys)
	return keys
}
