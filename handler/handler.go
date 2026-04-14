package handler

import (
	"context"
	"log"
	"sync"

	"github.com/jianyuelab/pt-backend/bgp"
	api "github.com/osrg/gobgp/v3/api"
)

// BGPHandler holds all received routes indexed by prefix then by peer IP.
type BGPHandler struct {
	collector *bgp.Collector
	mu        sync.RWMutex
	// store[prefix][peerIP] = latest analysis received from that peer
	store map[string]map[string]*bgp.PrefixAnalysis
}

func NewBGPHandler(collector *bgp.Collector) *BGPHandler {
	return &BGPHandler{
		collector: collector,
		store:     make(map[string]map[string]*bgp.PrefixAnalysis),
	}
}

// StartMonitoring watches BGP events and populates the per-peer store.
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
				if peerMap, ok := h.store[analysis.Prefix]; ok {
					delete(peerMap, analysis.Neighbor)
					if len(peerMap) == 0 {
						delete(h.store, analysis.Prefix)
					}
				}
			} else {
				if _, ok := h.store[analysis.Prefix]; !ok {
					h.store[analysis.Prefix] = make(map[string]*bgp.PrefixAnalysis)
				}
				h.store[analysis.Prefix][analysis.Neighbor] = analysis
			}
			h.mu.Unlock()
		})
		if err != nil {
			log.Printf("Watch error: %v", err)
		}
	}()
}
