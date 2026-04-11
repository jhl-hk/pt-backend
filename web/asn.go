package web

import (
	"cmp"
	"encoding/json"
	"net/http"
	"net/netip"
	"slices"
	"strconv"

	"github.com/jianyuelab/pt-backend/handler"
)

type Server struct {
	index map[int][]handler.PrefixPath
}

func NewServer(index map[int][]handler.PrefixPath) *Server {
	return &Server{index: index}
}

func (s *Server) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/v1/asn/{asn}", s.handleASN)
}

type prefixResponse struct {
	Prefix string `json:"prefix"`
	Path   []int  `json:"path"`
}

type asnResponse struct {
	ASN      int              `json:"asn"`
	Prefixes []prefixResponse `json:"prefixes"`
}

func (s *Server) handleASN(w http.ResponseWriter, r *http.Request) {
	asnStr := r.PathValue("asn")
	asn, err := strconv.Atoi(asnStr)
	if err != nil {
		http.Error(w, "invalid asn", http.StatusBadRequest)
		return
	}

	paths := slices.Clone(s.index[asn])
	slices.SortFunc(paths, func(a, b handler.PrefixPath) int {
		pa, _ := netip.ParsePrefix(a.Prefix)
		pb, _ := netip.ParsePrefix(b.Prefix)
		// IPv4 before IPv6
		a4, b4 := pa.Addr().Is4(), pb.Addr().Is4()
		if a4 != b4 {
			if a4 {
				return -1
			}
			return 1
		}
		// Numerical order by address, then prefix length
		if c := pa.Addr().Compare(pb.Addr()); c != 0 {
			return c
		}
		return cmp.Compare(pa.Bits(), pb.Bits())
	})

	resp := asnResponse{
		ASN:      asn,
		Prefixes: make([]prefixResponse, 0, len(paths)),
	}
	for _, p := range paths {
		resp.Prefixes = append(resp.Prefixes, prefixResponse{
			Prefix: p.Prefix,
			Path:   p.Path,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}
