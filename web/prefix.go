package web

import (
	"cmp"
	"encoding/json"
	"net/http"
	"net/netip"
	"slices"

	"github.com/jianyuelab/pt-backend/db"
)

type prefixStats struct {
	V4Count int   `json:"v4_count"`
	V6Count int   `json:"v6_count"`
	V4Size  int64 `json:"v4_size"` // /24 equivalents
	V6Size  int64 `json:"v6_size"` // /48 equivalents
}

// removeCovered returns only prefixes that are not covered by any other
// prefix in the slice. Prefixes are sorted most-general-first; any prefix
// whose network address falls inside an already-accepted (shorter) prefix
// is dropped.
func removeCovered(pfxs []netip.Prefix) []netip.Prefix {
	slices.SortFunc(pfxs, func(a, b netip.Prefix) int {
		return cmp.Compare(a.Bits(), b.Bits())
	})
	out := pfxs[:0]
	for _, p := range pfxs {
		covered := false
		for _, r := range out {
			if r.Bits() <= p.Bits() && r.Contains(p.Addr()) {
				covered = true
				break
			}
		}
		if !covered {
			out = append(out, p)
		}
	}
	return out
}

func computePrefixStats(prefixes []string) prefixStats {
	var v4, v6 []netip.Prefix
	for _, p := range prefixes {
		pfx, err := netip.ParsePrefix(p)
		if err != nil {
			continue
		}
		pfx = pfx.Masked() // normalise to network address
		if pfx.Addr().Is4() {
			v4 = append(v4, pfx)
		} else {
			v6 = append(v6, pfx)
		}
	}
	v4 = removeCovered(v4)
	v6 = removeCovered(v6)

	var s prefixStats
	s.V4Count = len(v4)
	s.V6Count = len(v6)
	for _, pfx := range v4 {
		bits := pfx.Bits()
		if bits <= 24 {
			s.V4Size += 1 << (24 - bits)
		} else {
			s.V4Size++
		}
	}
	for _, pfx := range v6 {
		bits := pfx.Bits()
		if bits <= 48 {
			s.V6Size += 1 << (48 - bits)
		} else {
			s.V6Size++
		}
	}
	return s
}

func (s *Server) handlePrefixCount(w http.ResponseWriter, r *http.Request) {
	if s.bunDB == nil {
		http.Error(w, "database unavailable", http.StatusServiceUnavailable)
		return
	}
	count, err := db.CountPrefixes(r.Context(), s.bunDB)
	if err != nil {
		http.Error(w, "query failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]int{"prefix_count": count})
}

type rankEntry struct {
	ASN         int    `json:"asn"`
	Name        string `json:"name,omitempty"`
	Short       string `json:"short,omitempty"`
	PrefixCount int    `json:"prefix_count"`
	prefixStats
}

func (s *Server) handleRankPrefix(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	index := s.index
	orgMap := s.orgMap
	s.mu.RUnlock()

	all := make([]rankEntry, 0, len(index))
	for asn, paths := range index {
		seen := make(map[string]struct{}, len(paths))
		for _, p := range paths {
			seen[p.Prefix] = struct{}{}
		}
		prefixes := make([]string, 0, len(seen))
		for p := range seen {
			prefixes = append(prefixes, p)
		}
		stats := computePrefixStats(prefixes)
		e := rankEntry{
			ASN:         asn,
			PrefixCount: len(prefixes),
			prefixStats: stats,
		}
		if orgMap != nil {
			if info, ok := orgMap[asn]; ok {
				e.Name = info.Name
			}
		}
		all = append(all, e)
	}

	top := func(less func(a, b rankEntry) int) []rankEntry {
		cp := slices.Clone(all)
		slices.SortFunc(cp, less)
		if len(cp) > 100 {
			cp = cp[:100]
		}
		return cp
	}

	resp := struct {
		V4 []rankEntry `json:"v4"`
		V6 []rankEntry `json:"v6"`
	}{
		V4: top(func(a, b rankEntry) int { return cmp.Compare(b.V4Size, a.V4Size) }),
		V6: top(func(a, b rankEntry) int { return cmp.Compare(b.V6Size, a.V6Size) }),
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}
