package web

import (
	"fmt"
	"log"
	"net/http"

	"github.com/jianyuelab/pt-backend/handler"
)

type Server struct {
	port    int
	handler *handler.BGPHandler
}

func NewServer(port int, h *handler.BGPHandler) *Server {
	return &Server{
		port:    port,
		handler: h,
	}
}

func (s *Server) Start() error {
	http.HandleFunc("/api/v1/prefix/", s.handlePrefix)
	http.HandleFunc("/api/v1/asn/", s.handleASN)

	addr := fmt.Sprintf(":%d", s.port)
	log.Printf("Web server starting on %s", addr)
	return http.ListenAndServe(addr, nil)
}
