package web

import (
	"cmp"
	"encoding/json"
	"net/http"
	"net/netip"
	"slices"
	"strconv"

	"github.com/jianyuelab/pt-backend/db"
	"github.com/jianyuelab/pt-backend/handler"
)

type orgResponse struct {
	Handle string `json:"handle,omitempty"`
	Name   string `json:"name,omitempty"`
}

type prefixResponse struct {
	Prefix string  `json:"prefix"`
	Paths  [][]int `json:"paths"`
}

type asnInfoResponse struct {
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
}

// parseASN extracts and validates the {asn} path value.
func parseASN(w http.ResponseWriter, r *http.Request) (int, bool) {
	asn, err := strconv.Atoi(r.PathValue("asn"))
	if err != nil {
		http.Error(w, "invalid asn", http.StatusBadRequest)
		return 0, false
	}
	return asn, true
}

// sortedPaths returns a clone of the index paths for asn, sorted IPv4-before-IPv6.
func (s *Server) sortedPaths(asn int) []handler.PrefixPath {
	s.mu.RLock()
	paths := slices.Clone(s.index[asn])
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
	return paths
}

// handleASNInfo returns basic AS metadata: name, country, org, relationships, prefix counts.
func (s *Server) handleASNInfo(w http.ResponseWriter, r *http.Request) {
	asn, ok := parseASN(w, r)
	if !ok {
		return
	}

	s.mu.RLock()
	rel := handler.CalculateRelationships(asn, s.index)
	orgMap := s.orgMap
	paths := s.index[asn]
	prefixSet := make(map[string]struct{}, len(paths))
	for _, p := range paths {
		prefixSet[p.Prefix] = struct{}{}
	}
	s.mu.RUnlock()

	prefixes := make([]string, 0, len(prefixSet))
	for p := range prefixSet {
		prefixes = append(prefixes, p)
	}

	resp := asnInfoResponse{
		ASN:           asn,
		Relationships: &rel,
		PrefixCount:   len(prefixes),
		prefixStats:   computePrefixStats(prefixes),
	}

	if s.bunDB != nil {
		var rec db.ASNRecord
		if err := s.bunDB.NewSelect().
			Model(&rec).
			Where("id = ?", asn).
			Scan(r.Context()); err == nil {
			if rec.Name != "" {
				resp.Name = rec.Name
			} else {
				resp.Name = rec.OrgName
			}
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

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// handleASNWhois returns the raw RIPE aut-num whois block as plain text.
func (s *Server) handleASNWhois(w http.ResponseWriter, r *http.Request) {
	asn, ok := parseASN(w, r)
	if !ok {
		return
	}

	if s.bunDB == nil {
		http.Error(w, "database unavailable", http.StatusServiceUnavailable)
		return
	}

	var rec db.ASNRecord
	if err := s.bunDB.NewSelect().
		Model(&rec).
		Column("whois").
		Where("id = ?", asn).
		Scan(r.Context()); err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write([]byte(rec.Whois))
}

// handleASNCIDR returns the prefix list with BGP paths for the given AS.
func (s *Server) handleASNCIDR(w http.ResponseWriter, r *http.Request) {
	asn, ok := parseASN(w, r)
	if !ok {
		return
	}

	paths := s.sortedPaths(asn)

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

	prefixes := make([]prefixResponse, 0, len(order))
	for _, prefix := range order {
		prefixes = append(prefixes, prefixResponse{
			Prefix: prefix,
			Paths:  grouped[prefix].paths,
		})
	}

	resp := struct {
		ASN      int              `json:"asn"`
		Prefixes []prefixResponse `json:"prefixes"`
	}{
		ASN:      asn,
		Prefixes: prefixes,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}
