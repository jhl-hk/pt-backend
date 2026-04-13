package web

import (
	"encoding/json"
	"net/http"

	"github.com/jianyuelab/pt-backend/db"
)

type tagASNEntry struct {
	ASN     int    `json:"asn"`
	Name    string `json:"name,omitempty"`
	Short   string `json:"short,omitempty"`
	OrgName string `json:"org_name,omitempty"`
	Country string `json:"country,omitempty"`
	Website string `json:"website,omitempty"`
	Tags    string `json:"tags"`
}

// handleTag returns all ASNs that carry the given tag.
// GET /api/v1/tag/{tag}
func (s *Server) handleTag(w http.ResponseWriter, r *http.Request) {
	tag := r.PathValue("tag")
	if tag == "" {
		http.Error(w, "missing tag", http.StatusBadRequest)
		return
	}

	if s.bunDB == nil {
		http.Error(w, "database unavailable", http.StatusServiceUnavailable)
		return
	}

	records, err := db.GetASNsByTag(r.Context(), s.bunDB, tag)
	if err != nil {
		http.Error(w, "query failed", http.StatusInternalServerError)
		return
	}

	s.mu.RLock()
	orgMap := s.orgMap
	s.mu.RUnlock()

	entries := make([]tagASNEntry, 0, len(records))
	for _, rec := range records {
		orgName := rec.OrgName
		if orgName == "" && orgMap != nil {
			if info, ok := orgMap[rec.ID]; ok {
				orgName = info.Name
			}
		}
		entries = append(entries, tagASNEntry{
			ASN:     rec.ID,
			Name:    rec.Name,
			Short:   rec.Short,
			OrgName: orgName,
			Country: rec.Country,
			Website: rec.Website,
			Tags:    rec.Tags,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"tag":  tag,
		"asns": entries,
	})
}
