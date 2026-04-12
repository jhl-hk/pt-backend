package web

import (
	"cmp"
	"encoding/json"
	"net/http"
	"net/netip"
	"slices"
	"strconv"
	"sync"

	"github.com/jianyuelab/pt-backend/db"
	"github.com/jianyuelab/pt-backend/handler"
	"github.com/uptrace/bun"
)

type Server struct {
	index  map[int][]handler.PrefixPath
	bunDB  *bun.DB
	mu     sync.RWMutex
	orgMap map[int]handler.OrgInfo
}

func NewServer(index map[int][]handler.PrefixPath, bunDB *bun.DB) *Server {
	return &Server{index: index, bunDB: bunDB}
}

func (s *Server) SetOrgMap(m map[int]handler.OrgInfo) {
	s.mu.Lock()
	s.orgMap = m
	s.mu.Unlock()
}

func (s *Server) SetIndex(index map[int][]handler.PrefixPath) {
	s.mu.Lock()
	s.index = index
	s.mu.Unlock()
}

func (s *Server) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/v1/asn/{asn}", s.handleASN)
	mux.HandleFunc("/api/v1/prefixes/count", s.handlePrefixCount)
	mux.HandleFunc("/api/v1/rank/prefix", s.handleRankPrefix)
}

type orgResponse struct {
	Handle string `json:"handle,omitempty"`
	Name   string `json:"name,omitempty"`
}

type prefixResponse struct {
	Prefix string  `json:"prefix"`
	Paths  [][]int `json:"paths"`
}

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

type asnResponse struct {
	ASN           int                    `json:"asn"`
	Name          string                 `json:"name,omitempty"`
	Short         string                 `json:"short,omitempty"`
	Country       string                 `json:"country,omitempty"`
	Website       string                 `json:"website,omitempty"`
	Tags          string                 `json:"tags"`
	Comments      string                 `json:"comments,omitempty"`
	Org           *orgResponse           `json:"org,omitempty"`
	SponsorOrg    *orgResponse           `json:"sponsor_org,omitempty"`
	Relationships *handler.Relationships `json:"relationships,omitempty"`
	PrefixCount   int                    `json:"prefix_count"`
	prefixStats
	Prefixes []prefixResponse `json:"prefixes"`
}

func (s *Server) handleASN(w http.ResponseWriter, r *http.Request) {
	asnStr := r.PathValue("asn")
	asn, err := strconv.Atoi(asnStr)
	if err != nil {
		http.Error(w, "invalid asn", http.StatusBadRequest)
		return
	}

	s.mu.RLock()
	paths := slices.Clone(s.index[asn])
	rel := handler.CalculateRelationships(asn, s.index)
	s.mu.RUnlock()
	slices.SortFunc(paths, func(a, b handler.PrefixPath) int {
		pa, _ := netip.ParsePrefix(a.Prefix)
		pb, _ := netip.ParsePrefix(b.Prefix)
		a4, b4 := pa.Addr().Is4(), pb.Addr().Is4()
		if a4 != b4 {
			if a4 {
				return -1
			}
			return 1
		}
		if c := pa.Addr().Compare(pb.Addr()); c != 0 {
			return c
		}
		return cmp.Compare(pa.Bits(), pb.Bits())
	})

	s.mu.RLock()
	orgMap := s.orgMap
	s.mu.RUnlock()

	resp := asnResponse{
		ASN:           asn,
		Relationships: &rel,
		Prefixes:      make([]prefixResponse, 0, len(paths)),
	}

	// Load DB record for name, short, tags, comments.
	if s.bunDB != nil {
		var rec db.ASNRecord
		if err := s.bunDB.NewSelect().
			Model(&rec).
			Where("id = ?", asn).
			Scan(r.Context()); err == nil {
			resp.Name = rec.Name
			resp.Short = rec.Short
			resp.Country = rec.Country
			resp.Website = rec.Website
			resp.Tags = rec.Tags
			resp.Comments = rec.Comments
		}
	}

	if orgMap != nil {
		if info, ok := orgMap[asn]; ok {
			resp.Org = &orgResponse{Handle: info.Handle, Name: info.Name}
			if info.SponsorHandle != "" && info.SponsorHandle != info.Handle {
				resp.SponsorOrg = &orgResponse{Handle: info.SponsorHandle, Name: info.SponsorName}
			}
		}
	}

	// Group paths by prefix (preserve sorted order of first occurrence).
	type entry struct{ paths [][]int }
	grouped := make(map[string]*entry)
	order := make([]string, 0, len(paths))
	for _, p := range paths {
		if e, ok := grouped[p.Prefix]; ok {
			e.paths = append(e.paths, p.Path)
		} else {
			grouped[p.Prefix] = &entry{paths: [][]int{p.Path}}
			order = append(order, p.Prefix)
		}
	}
	for _, prefix := range order {
		resp.Prefixes = append(resp.Prefixes, prefixResponse{
			Prefix: prefix,
			Paths:  grouped[prefix].paths,
		})
	}
	resp.PrefixCount = len(resp.Prefixes)
	resp.prefixStats = computePrefixStats(order)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
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
