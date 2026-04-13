package web

import (
	"net/http"
	"sync"

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
	mux.HandleFunc("/api/v1/asn/{asn}", s.handleASNInfo)
	mux.HandleFunc("/api/v1/whois/{asn}", s.handleASNWhois)
	mux.HandleFunc("/api/v1/cidr/{asn}", s.handleASNCIDR)
	mux.HandleFunc("/api/v1/tag/{tag}", s.handleTag)
	mux.HandleFunc("/api/v1/prefixes/count", s.handlePrefixCount)
	mux.HandleFunc("/api/v1/rank/prefix", s.handleRankPrefix)
	mux.HandleFunc("/api/v1/rank/downstream", s.handleRankDownstream)
	mux.HandleFunc("/api/v1/rank/peer", s.handleRankPeer)
	mux.HandleFunc("/api/v1/rank/ascone", s.handleRankASCone)
}
