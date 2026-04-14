package web

import (
	"fmt"
	"log"
	"net/http"
)

type Server struct {
	port int
}

func NewServer(port int) *Server {
	return &Server{
		port: port,
	}
}

func (s *Server) Start() error {
	http.HandleFunc("/api/v1/prefixes", s.handleListPrefixes)

	addr := fmt.Sprintf(":%d", s.port)
	log.Printf("Web server starting on %s", addr)
	return http.ListenAndServe(addr, nil)
}

func (s *Server) handleListPrefixes(w http.ResponseWriter, r *http.Request) {
	// Implement list prefixes from DB
	fmt.Fprintf(w, "List of prefixes analyzed")
}
