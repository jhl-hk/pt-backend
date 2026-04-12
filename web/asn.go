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
}

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
	Tags          string                 `json:"tags,omitempty"`
	Comments      string                 `json:"comments,omitempty"`
	Org           *orgResponse           `json:"org,omitempty"`
	SponsorOrg    *orgResponse           `json:"sponsor_org,omitempty"`
	Relationships *handler.Relationships `json:"relationships,omitempty"`
	Prefixes      []prefixResponse       `json:"prefixes"`
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

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}
