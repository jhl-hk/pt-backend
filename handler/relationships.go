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
// given ASN from the routing index using a three-priority algorithm:
//
// Priority 1 — Tier-1 anchor rule:
//   - ASNs in Tier1ASNs (excluding RootASN) have no upstream providers.
//   - Any ASN adjacent to a Tier-1-only ASN is classified as its peer.
//   - A non-Tier-1 ASN always treats a Tier-1 neighbour as its upstream.
//
// Priority 2 — Export Visibility Test (Gao-Rexford):
//
//	For each adjacent non-Tier-1 ASN Y, inspect paths that contain a Tier-1:
//	  • asn appears LEFT of Y  (asn carries Y's routes toward T1) → asn is Y's provider
//	                                                              → Y is asn's downstream
//	  • Y   appears LEFT of asn (Y carries asn's routes toward T1) → Y is asn's upstream
//	  • Both directions seen                                       → Peer
//	  • No T1-path evidence                                        → Peer (default safe)
//
// Downstream is a subset of Peers (peers includes all non-upstream neighbours).
//
// AS_PATH direction note: in [A, B, C, D] (D = origin), the route travelled
// D→C→B→A, so LEFT of X = X announced its routes there = X's upstream;
// RIGHT of X = announced to X = X's downstream/customer.
func CalculateRelationships(asn int, index map[int][]PrefixPath) Relationships {
	// True Tier-1s: have no upstream. Tier1ASNs includes RootASN (215172) which
	// does have upstreams, so we exclude it from the "no-upstream" rule.
	isASNTier1 := Tier1ASNs[asn] && asn != RootASN

	// Export Visibility sets, populated only from T1-containing paths.
	//   providesFor[Y]  = asn is immediately left of Y in a T1-path
	//                     → asn provides transit for Y → Y is asn's downstream
	//   receivesFrom[Y] = Y is immediately left of asn in a T1-path
	//                     → Y provides transit for asn → Y is asn's upstream
	providesFor := make(map[int]bool)
	receivesFrom := make(map[int]bool)

	adjacentSet := make(map[int]bool)   // immediate left + right neighbours in any path
	rawDownstream := make(map[int]bool) // originASNs whose paths transit through asn

	for originASN, prefixes := range index {
		for _, pp := range prefixes {
			asnIdx := -1
			for i, a := range pp.Path {
				if a == asn {
					asnIdx = i
					break
				}
			}
			if asnIdx == -1 {
				continue
			}

			// isSessionPeer: asn sits at index 1 in a RootASN-anchored path.
			// buildPath keeps RootASN when the first hop is non-T1, producing
			// paths like [215172, SESSION_PEER, X, Y]. SESSION_PEER is 215172's
			// direct BGP session peer (IXP route server etc.) — it is NOT a
			// transit provider for the ASNs that follow it in the path.
			isSessionPeer := asnIdx == 1 && len(pp.Path) > 0 && pp.Path[0] == RootASN

			// isEdgeObserver: non-T1 feed AS at index 0 (e.g. 211575, 51087,
			// 202734). The dedicated collector has a direct BGP session with
			// each feed AS; that feed AS prepends itself at position 0. It is
			// at the BOTTOM of the hierarchy for this path — it is NOT a
			// transit provider for the ASNs to its right.
			isEdgeObserver := asnIdx == 0 && !Tier1ASNs[asn]

			// leftIsEdgeObserver: asn sits at index 1 and the path starts with
			// a non-T1 edge observer at index 0, so the "left" neighbour is
			// merely the collector session peer, not a genuine transit provider.
			leftIsEdgeObserver := asnIdx == 1 && len(pp.Path) > 0 && !Tier1ASNs[pp.Path[0]]

			var left, right int
			if asnIdx > 0 {
				left = pp.Path[asnIdx-1]
				if left != 0 {
					adjacentSet[left] = true
				}
			}
			if asnIdx < len(pp.Path)-1 {
				right = pp.Path[asnIdx+1]
				if right != 0 {
					adjacentSet[right] = true
					// Record raw downstream only when asn genuinely carries
					// originASN's routes as a transit provider, not when it is
					// an edge observer or a BGP session peer of RootASN.
					if originASN != asn && !isEdgeObserver && !isSessionPeer {
						rawDownstream[originASN] = true
					}
				}
			}

			// Export Visibility Test.
			// Anchor = a real Tier-1 (44324 etc.) OR RootASN (215172).
			pathHasAnchor := false
			for _, a := range pp.Path {
				if Tier1ASNs[a] { // includes RootASN
					pathHasAnchor = true
					break
				}
			}
			if !pathHasAnchor {
				continue
			}
			isRootAnchored := pp.Path[0] == RootASN
			// Edge observers do not provide transit for ASNs to their right.
			if right != 0 && asn != RootASN && !isEdgeObserver {
				if !isRootAnchored || asnIdx >= 2 {
					providesFor[right] = true
				}
			}
			// The left neighbour being an edge observer does not make asn a
			// receiver of transit from that edge observer.
			if left != 0 && !Tier1ASNs[left] && !leftIsEdgeObserver {
				if !isRootAnchored || asnIdx >= 3 {
					receivesFrom[left] = true
				}
			}
		}
	}

	upstreamSet := make(map[int]bool)
	downstreamSet := make(map[int]bool)
	peerSet := make(map[int]bool)

	for y := range adjacentSet {
		if y == 0 {
			continue
		}

		// Priority 1a: queried ASN is a Tier-1 → it has no upstream; all
		// adjacent ASNs (including other Tier-1s) are treated as peers.
		if isASNTier1 {
			peerSet[y] = true
			continue
		}

		// Priority 1b: neighbour is a Tier-1 → always asn's upstream.
		if Tier1ASNs[y] && y != RootASN {
			upstreamSet[y] = true
			continue
		}

		// Priority 2: Export Visibility Test.
		xProvidesY := providesFor[y]
		yProvidesX := receivesFrom[y]

		switch {
		case xProvidesY && !yProvidesX:
			// asn provides transit for Y → Y is asn's customer/downstream.
			downstreamSet[y] = true
			peerSet[y] = true // peers includes downstream
		case yProvidesX && !xProvidesY:
			// Y provides transit for asn → Y is asn's upstream.
			upstreamSet[y] = true
		default:
			// Mutual or no T1-path evidence → peer.
			peerSet[y] = true
		}
	}

	// Non-adjacent downstreams: originASNs not directly adjacent to asn but
	// whose routes transit through it. These are always customers.
	for y := range rawDownstream {
		if !adjacentSet[y] && !upstreamSet[y] {
			downstreamSet[y] = true
			peerSet[y] = true
		}
	}

	upstream := sortedKeys(upstreamSet)
	downstream := sortedKeys(downstreamSet)
	peers := sortedKeys(peerSet)

	// Cone: asn itself plus all its downstream customers.
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

// BulkStats contains pre-computed relationship counts for a single ASN.
// It mirrors the full Relationships struct but omits the member lists to keep
// memory usage low when computing stats for every ASN in the index at once.
type BulkStats struct {
	UpstreamCount   int
	DownstreamCount int
	PeerCount       int
	ConeSize        int
}

// ComputeBulkStats derives relationship stats for every ASN that appears in
// the routing index in a single O(paths) pass, avoiding the O(ASNs×paths)
// cost of calling CalculateRelationships for each ASN individually.
func ComputeBulkStats(index map[int][]PrefixPath) map[int]BulkStats {
	type asnData struct {
		adjacent      map[int]bool
		providesFor   map[int]bool // T1-path: this ASN left of them → provides transit
		receivesFrom  map[int]bool // T1-path: they left of this ASN → they provide transit
		rawDownstream map[int]bool
	}

	data := make(map[int]*asnData)
	ensure := func(asn int) *asnData {
		if data[asn] == nil {
			data[asn] = &asnData{
				adjacent:      make(map[int]bool),
				providesFor:   make(map[int]bool),
				receivesFrom:  make(map[int]bool),
				rawDownstream: make(map[int]bool),
			}
		}
		return data[asn]
	}

	for originASN, prefixes := range index {
		for _, pp := range prefixes {
			// Same anchor logic as CalculateRelationships: T1 OR RootASN.
			pathHasAnchor := false
			for _, a := range pp.Path {
				if Tier1ASNs[a] {
					pathHasAnchor = true
					break
				}
			}

			for i, asn := range pp.Path {
				if asn == 0 {
					continue
				}
				d := ensure(asn)

				var left, right int
				if i > 0 {
					left = pp.Path[i-1]
				}
				if i < len(pp.Path)-1 {
					right = pp.Path[i+1]
				}

				if left != 0 {
					d.adjacent[left] = true
				}
				isSessionPeer := i == 1 && len(pp.Path) > 0 && pp.Path[0] == RootASN
				isEdgeObserver := i == 0 && !Tier1ASNs[asn]
				leftIsEdgeObserver := i == 1 && len(pp.Path) > 0 && !Tier1ASNs[pp.Path[0]]
				if right != 0 {
					d.adjacent[right] = true
					if originASN != asn && !isEdgeObserver && !isSessionPeer {
						d.rawDownstream[originASN] = true
					}
				}

				if pathHasAnchor {
					isRootAnchored := pp.Path[0] == RootASN
					if right != 0 && asn != RootASN && !isEdgeObserver {
						if !isRootAnchored || i >= 2 {
							d.providesFor[right] = true
						}
					}
					if left != 0 && !Tier1ASNs[left] && !leftIsEdgeObserver {
						if !isRootAnchored || i >= 3 {
							d.receivesFrom[left] = true
						}
					}
				}
			}
		}
	}

	result := make(map[int]BulkStats, len(data))
	for asn, d := range data {
		isT1 := Tier1ASNs[asn] && asn != RootASN

		upstream := make(map[int]bool)
		downstream := make(map[int]bool)
		peers := make(map[int]bool)

		for y := range d.adjacent {
			if y == 0 {
				continue
			}
			if isT1 {
				peers[y] = true
				continue
			}
			if Tier1ASNs[y] && y != RootASN {
				upstream[y] = true
				continue
			}
			switch {
			case d.providesFor[y] && !d.receivesFrom[y]:
				downstream[y] = true
				peers[y] = true
			case d.receivesFrom[y] && !d.providesFor[y]:
				upstream[y] = true
			default:
				peers[y] = true
			}
		}
		for y := range d.rawDownstream {
			if !d.adjacent[y] && !upstream[y] {
				downstream[y] = true
				peers[y] = true
			}
		}

		result[asn] = BulkStats{
			UpstreamCount:   len(upstream),
			DownstreamCount: len(downstream),
			PeerCount:       len(peers),
			ConeSize:        len(downstream) + 1,
		}
	}
	return result
}

func sortedKeys(m map[int]bool) []int {
	keys := make([]int, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Ints(keys)
	return keys
}
