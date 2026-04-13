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

type asnResponse struct {
	ASN           int                    `json:"asn"`
	Name          string                 `json:"name,omitempty"`
	Short         string                 `json:"short,omitempty"`
	Country       string                 `json:"country,omitempty"`
	Website       string                 `json:"website,omitempty"`
	Tags          string                 `json:"tags"`
	Comments      string                 `json:"comments,omitempty"`
	Whois         string                 `json:"whois,omitempty"`
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
			resp.Whois = rec.Whois
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
