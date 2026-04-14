package web

import (
	"encoding/json"
	"net/http"
	"strings"
)

// handlePrefix services all /api/v1/prefix/{cidr}[/{subresource}] requests.
//
//	GET /api/v1/prefix/1.1.1.0/24            → summary
//	GET /api/v1/prefix/1.1.1.0/24/routes     → all routes from all peers
//	GET /api/v1/prefix/1.1.1.0/24/peers      → list of announcing peers
//	GET /api/v1/prefix/1.1.1.0/24/upstreams  → upstream / tier-1 ASes
//	GET /api/v1/prefix/1.1.1.0/24/downstreams → more-specific (sub) prefixes
func (s *Server) handlePrefix(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	subPath := strings.TrimPrefix(r.URL.Path, "/api/v1/prefix/")
	if subPath == "" {
		http.Error(w, "prefix required", http.StatusBadRequest)
		return
	}

	subResources := map[string]bool{
		"routes": true, "peers": true, "upstreams": true, "downstreams": true, "graph": true,
	}

	var prefix, subResource string
	if idx := strings.LastIndex(subPath, "/"); idx > 0 {
		if tail := subPath[idx+1:]; subResources[tail] {
			subResource = tail
			prefix = subPath[:idx]
		} else {
			prefix = subPath
		}
	} else {
		prefix = subPath
	}

	w.Header().Set("Content-Type", "application/json")

	switch subResource {
	case "routes":
		routes := s.handler.GetPrefixRoutes(prefix)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"prefix": prefix,
			"count":  len(routes),
			"routes": routes,
		})

	case "peers":
		peers := s.handler.GetPrefixPeers(prefix)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"prefix": prefix,
			"count":  len(peers),
			"peers":  peers,
		})

	case "upstreams":
		summary, ok := s.handler.GetPrefixSummary(prefix)
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]string{"error": "prefix not found"})
			return
		}
		json.NewEncoder(w).Encode(map[string]interface{}{
			"prefix":     prefix,
			"upstreams":  summary.Upstreams,
			"tier1_ases": summary.Tier1ASes,
		})

	case "downstreams":
		subs := s.handler.GetSubPrefixes(prefix)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"prefix":       prefix,
			"count":        len(subs),
			"sub_prefixes": subs,
		})

	case "graph":
		graph, ok := s.handler.GetPrefixGraph(prefix)
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]string{"error": "prefix not found"})
			return
		}
		json.NewEncoder(w).Encode(graph)

	default:
		summary, ok := s.handler.GetPrefixSummary(prefix)
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]string{"error": "prefix not found"})
			return
		}
		json.NewEncoder(w).Encode(summary)
	}
}
