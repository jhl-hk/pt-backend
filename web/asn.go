package web

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
)

// handleASN services all /api/v1/asn/{asn}[/{subresource}] requests.
//
//	GET /api/v1/asn/13335            → full ASN summary
//	GET /api/v1/asn/13335/prefixes   → originated prefixes
//	GET /api/v1/asn/13335/upstreams  → upstream providers
//	GET /api/v1/asn/13335/downstreams → downstream ASNs (customers)
//	GET /api/v1/asn/13335/peers      → announcing collector peers
func (s *Server) handleASN(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	subPath := strings.TrimPrefix(r.URL.Path, "/api/v1/asn/")
	if subPath == "" {
		http.Error(w, "ASN required", http.StatusBadRequest)
		return
	}

	subResources := map[string]bool{
		"prefixes": true, "upstreams": true, "downstreams": true, "peers": true, "graph": true,
	}

	var asnStr, subResource string
	if idx := strings.LastIndex(subPath, "/"); idx > 0 {
		if tail := subPath[idx+1:]; subResources[tail] {
			subResource = tail
			asnStr = subPath[:idx]
		} else {
			asnStr = subPath
		}
	} else {
		asnStr = subPath
	}

	asnVal, err := strconv.ParseUint(asnStr, 10, 32)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "invalid ASN"})
		return
	}
	asn := uint32(asnVal)

	w.Header().Set("Content-Type", "application/json")

	switch subResource {
	case "prefixes":
		prefixes := s.handler.GetASNPrefixes(asn)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"asn":      asn,
			"count":    len(prefixes),
			"prefixes": prefixes,
		})

	case "upstreams":
		upstreams := s.handler.GetASNUpstreams(asn)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"asn":       asn,
			"upstreams": upstreams,
		})

	case "downstreams":
		downstreams := s.handler.GetASNDownstreams(asn)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"asn":         asn,
			"downstreams": downstreams,
		})

	case "peers":
		peers := s.handler.GetASNPeers(asn)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"asn":   asn,
			"count": len(peers),
			"peers": peers,
		})

	case "graph":
		graph := s.handler.GetASNGraph(asn)
		json.NewEncoder(w).Encode(graph)

	default:
		summary, ok := s.handler.GetASNSummary(asn)
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]string{"error": "ASN not found"})
			return
		}
		json.NewEncoder(w).Encode(summary)
	}
}
