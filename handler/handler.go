package handler

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/jianyuelab/pt-backend/bgp"
	"github.com/jianyuelab/pt-backend/db"
	api "github.com/osrg/gobgp/v3/api"
)

// dbOp queues a single BGP event for asynchronous DB persistence.
type dbOp struct {
	withdraw bool
	analysis *bgp.PrefixAnalysis
}

// BGPHandler holds all received routes indexed by prefix then by peer IP,
// plus three secondary indices that make ASN queries sub-linear.
type BGPHandler struct {
	collector *bgp.Collector
	mu        sync.RWMutex
	// store[prefix][peerIP] = latest analysis received from that peer
	store map[string]map[string]*bgp.PrefixAnalysis

	// originIndex[originAS] = set of prefixes originated by that AS.
	originIndex map[uint32]map[string]struct{}

	// transitIndex[transitAS][originAS] = {} means transitAS appears in at least
	// one route's UpstreamChain for a prefix originated by originAS.
	// Eventually consistent: stale entries are filtered at read time.
	transitIndex map[uint32]map[uint32]struct{}

	// adjacentIndex[asn] = set of ASNs directly adjacent to asn in any collected AS path.
	adjacentIndex map[uint32]map[uint32]struct{}

	// adjacentCount[canonicalPair] = number of routes containing this adjacency.
	// When the count drops to zero the edge is removed from adjacentIndex.
	adjacentCount map[[2]uint32]uint32

	// DB integration — nil when no database is configured.
	database *db.Database
	dbCh     chan dbOp // buffered async write channel
}

func NewBGPHandler(collector *bgp.Collector, database *db.Database) *BGPHandler {
	h := &BGPHandler{
		collector:     collector,
		store:         make(map[string]map[string]*bgp.PrefixAnalysis),
		originIndex:   make(map[uint32]map[string]struct{}),
		transitIndex:  make(map[uint32]map[uint32]struct{}),
		adjacentIndex: make(map[uint32]map[uint32]struct{}),
		adjacentCount: make(map[[2]uint32]uint32),
		database:      database,
		dbCh:          make(chan dbOp, 10000),
	}
	if database != nil {
		go h.runDBWriter(context.Background())
	}
	return h
}

// LoadFromDB hydrates the in-memory store and all secondary indices from the DB.
// Call this once at startup, before StartMonitoring, when a DB is configured.
func (h *BGPHandler) LoadFromDB(ctx context.Context) error {
	if h.database == nil {
		return nil
	}
	routes, err := h.database.LoadAll(ctx)
	if err != nil {
		return err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, r := range routes {
		a := routeFromDB(r)
		if h.store[a.Prefix] == nil {
			h.store[a.Prefix] = make(map[string]*bgp.PrefixAnalysis)
		}
		h.store[a.Prefix][a.Neighbor] = a
		h.addToIndices(a)
	}
	log.Printf("Loaded %d routes from DB", len(routes))
	return nil
}

// StartMonitoring watches BGP events and populates the per-peer store and indices.
func (h *BGPHandler) StartMonitoring(ctx context.Context) {
	go func() {
		err := h.collector.WatchEvents(ctx, func(path *api.Path) {
			analysis, err := bgp.AnalyzePath(path)
			if err != nil {
				log.Printf("Analysis error: %v", err)
				return
			}
			h.mu.Lock()
			if analysis.IsWithdraw {
				if peerMap, exists := h.store[analysis.Prefix]; exists {
					if old, peerExists := peerMap[analysis.Neighbor]; peerExists {
						delete(peerMap, analysis.Neighbor)
						if len(peerMap) == 0 {
							delete(h.store, analysis.Prefix)
						}
						h.removeFromIndices(old)
					}
				}
			} else {
				if h.store[analysis.Prefix] == nil {
					h.store[analysis.Prefix] = make(map[string]*bgp.PrefixAnalysis)
				}
				old := h.store[analysis.Prefix][analysis.Neighbor]
				h.store[analysis.Prefix][analysis.Neighbor] = analysis
				if old != nil {
					h.removeFromIndices(old)
				}
				h.addToIndices(analysis)
			}
			h.mu.Unlock()

			// Forward to async DB writer — non-blocking to avoid slowing the BGP loop.
			if h.database != nil {
				select {
				case h.dbCh <- dbOp{withdraw: analysis.IsWithdraw, analysis: analysis}:
				default:
					log.Printf("DB write channel full, dropping %s/%s", analysis.Prefix, analysis.Neighbor)
				}
			}
		})
		if err != nil {
			log.Printf("Watch error: %v", err)
		}
	}()
}

// runDBWriter drains dbCh and flushes to the DB in batches.
// Batches are flushed every 500 ms or when 200 operations have accumulated.
func (h *BGPHandler) runDBWriter(ctx context.Context) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	var upserts []*db.Route
	var deletes [][2]string // (prefix, peerIP) pairs

	flush := func() {
		if len(upserts) > 0 {
			if err := h.database.BulkUpsert(ctx, upserts); err != nil {
				log.Printf("DB bulk upsert error: %v", err)
			}
			upserts = upserts[:0]
		}
		for _, key := range deletes {
			if err := h.database.Delete(ctx, key[0], key[1]); err != nil {
				log.Printf("DB delete error (%s/%s): %v", key[0], key[1], err)
			}
		}
		deletes = deletes[:0]
	}

	for {
		select {
		case op := <-h.dbCh:
			if op.withdraw {
				deletes = append(deletes, [2]string{op.analysis.Prefix, op.analysis.Neighbor})
			} else {
				upserts = append(upserts, routeToDB(op.analysis))
			}
			if len(upserts)+len(deletes) >= 200 {
				flush()
			}
		case <-ticker.C:
			flush()
		case <-ctx.Done():
			flush()
			return
		}
	}
}

// addToIndices registers a route's contributions to all three secondary indices.
// Must be called with mu held for writing.
func (h *BGPHandler) addToIndices(a *bgp.PrefixAnalysis) {
	if a.OriginAS != 0 {
		if h.originIndex[a.OriginAS] == nil {
			h.originIndex[a.OriginAS] = make(map[string]struct{})
		}
		h.originIndex[a.OriginAS][a.Prefix] = struct{}{}
		for _, u := range a.UpstreamChain {
			if u == 0 {
				continue
			}
			if h.transitIndex[u] == nil {
				h.transitIndex[u] = make(map[uint32]struct{})
			}
			h.transitIndex[u][a.OriginAS] = struct{}{}
		}
	}
	h.addAdjacencies(a.ASPath)
}

// canonicalPair returns a direction-independent key for adjacentCount by placing
// the smaller ASN first.
func canonicalPair(a, b uint32) [2]uint32 {
	if a <= b {
		return [2]uint32{a, b}
	}
	return [2]uint32{b, a}
}

// addAdjacencies records every consecutive ASN pair in path.
// Must be called with mu held for writing.
func (h *BGPHandler) addAdjacencies(path []uint32) {
	for i := 0; i < len(path)-1; i++ {
		a, b := path[i], path[i+1]
		if a == 0 || b == 0 {
			continue
		}
		pair := canonicalPair(a, b)
		h.adjacentCount[pair]++
		if h.adjacentCount[pair] == 1 {
			if h.adjacentIndex[a] == nil {
				h.adjacentIndex[a] = make(map[uint32]struct{})
			}
			if h.adjacentIndex[b] == nil {
				h.adjacentIndex[b] = make(map[uint32]struct{})
			}
			h.adjacentIndex[a][b] = struct{}{}
			h.adjacentIndex[b][a] = struct{}{}
		}
	}
}

// removeAdjacencies decrements refcounts for each consecutive pair in path
// and removes edges whose count reaches zero.
// Must be called with mu held for writing.
func (h *BGPHandler) removeAdjacencies(path []uint32) {
	for i := 0; i < len(path)-1; i++ {
		a, b := path[i], path[i+1]
		if a == 0 || b == 0 {
			continue
		}
		pair := canonicalPair(a, b)
		if h.adjacentCount[pair] == 0 {
			continue // defensive: should not happen in a consistent state
		}
		h.adjacentCount[pair]--
		if h.adjacentCount[pair] == 0 {
			delete(h.adjacentCount, pair)
			delete(h.adjacentIndex[a], b)
			if len(h.adjacentIndex[a]) == 0 {
				delete(h.adjacentIndex, a)
			}
			delete(h.adjacentIndex[b], a)
			if len(h.adjacentIndex[b]) == 0 {
				delete(h.adjacentIndex, b)
			}
		}
	}
}

// removeFromIndices tears down all index contributions of a single route entry.
// Must be called with mu held for writing, after the route has already been
// removed from (or overwritten in) the store.
func (h *BGPHandler) removeFromIndices(old *bgp.PrefixAnalysis) {
	if old.OriginAS != 0 {
		// Only remove the prefix from originIndex if no remaining route for
		// this prefix still has old.OriginAS as its origin.
		stillPresent := false
		if peerMap, ok := h.store[old.Prefix]; ok {
			for _, a := range peerMap {
				if a.OriginAS == old.OriginAS {
					stillPresent = true
					break
				}
			}
		}
		if !stillPresent {
			if s, ok := h.originIndex[old.OriginAS]; ok {
				delete(s, old.Prefix)
				if len(s) == 0 {
					delete(h.originIndex, old.OriginAS)
				}
			}
		}
	}
	// transitIndex is eventually consistent: we don't tear it down per-route.
	// Stale entries (transit ASN no longer used by an origin) are verified at
	// read time inside GetASNDownstreams and GetASNPeers.
	h.removeAdjacencies(old.ASPath)
}

// routeToDB converts a PrefixAnalysis to a db.Route for persistence.
func routeToDB(a *bgp.PrefixAnalysis) *db.Route {
	communities := make([]uint32, len(a.Communities))
	copy(communities, a.Communities)
	upstreamChain := make([]uint32, len(a.UpstreamChain))
	copy(upstreamChain, a.UpstreamChain)
	asPath := make([]uint32, len(a.ASPath))
	copy(asPath, a.ASPath)
	return &db.Route{
		Prefix:         a.Prefix,
		PeerIP:         a.Neighbor,
		PeerASN:        a.PeerASN,
		OriginAS:       a.OriginAS,
		ASPath:         asPath,
		UpstreamChain:  upstreamChain,
		DirectUpstream: a.DirectUpstream,
		Tier1Found:     a.Tier1Found,
		Communities:    communities,
		NextHop:        a.NextHop,
		Origin:         a.Origin,
		ReceivedAt:     a.ReceivedAt,
	}
}

// routeFromDB converts a db.Route back to a PrefixAnalysis for in-memory use.
func routeFromDB(r *db.Route) *bgp.PrefixAnalysis {
	communities := make([]uint32, len(r.Communities))
	copy(communities, r.Communities)
	upstreamChain := make([]uint32, len(r.UpstreamChain))
	copy(upstreamChain, r.UpstreamChain)
	asPath := make([]uint32, len(r.ASPath))
	copy(asPath, r.ASPath)
	return &bgp.PrefixAnalysis{
		Prefix:         r.Prefix,
		Neighbor:       r.PeerIP,
		PeerASN:        r.PeerASN,
		OriginAS:       r.OriginAS,
		ASPath:         asPath,
		UpstreamChain:  upstreamChain,
		DirectUpstream: r.DirectUpstream,
		Tier1Found:     r.Tier1Found,
		Communities:    communities,
		NextHop:        r.NextHop,
		Origin:         r.Origin,
		ReceivedAt:     r.ReceivedAt,
	}
}
