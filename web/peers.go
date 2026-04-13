package web

import (
	"cmp"
	"encoding/json"
	"net/http"
	"slices"

	"github.com/jianyuelab/pt-backend/handler"
)

type relRankEntry struct {
	ASN   int    `json:"asn"`
	Name  string `json:"name,omitempty"`
	Count int    `json:"count"`
}

func (s *Server) handleRankDownstream(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	index := s.index
	orgMap := s.orgMap
	s.mu.RUnlock()

	stats := handler.ComputeBulkStats(index)
	entries := rankRelEntries(stats, orgMap, func(b handler.BulkStats) int { return b.DownstreamCount })

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(entries)
}

func (s *Server) handleRankPeer(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	index := s.index
	orgMap := s.orgMap
	s.mu.RUnlock()

	stats := handler.ComputeBulkStats(index)
	entries := rankRelEntries(stats, orgMap, func(b handler.BulkStats) int { return b.PeerCount })

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(entries)
}

func (s *Server) handleRankASCone(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	index := s.index
	orgMap := s.orgMap
	s.mu.RUnlock()

	stats := handler.ComputeBulkStats(index)
	entries := rankRelEntries(stats, orgMap, func(b handler.BulkStats) int { return b.ConeSize })

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(entries)
}

// rankRelEntries builds a top-100 ranking slice from bulk stats using the
// provided score function.
func rankRelEntries(
	stats map[int]handler.BulkStats,
	orgMap map[int]handler.OrgInfo,
	score func(handler.BulkStats) int,
) []relRankEntry {
	entries := make([]relRankEntry, 0, len(stats))
	for asn, s := range stats {
		e := relRankEntry{ASN: asn, Count: score(s)}
		if orgMap != nil {
			if info, ok := orgMap[asn]; ok {
				e.Name = info.Name
			}
		}
		entries = append(entries, e)
	}
	slices.SortFunc(entries, func(a, b relRankEntry) int {
		return cmp.Compare(b.Count, a.Count)
	})
	if len(entries) > 100 {
		entries = entries[:100]
	}
	return entries
}
