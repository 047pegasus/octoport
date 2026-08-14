package api

import (
	"encoding/json"
	"net/http"
	"strconv"
)

// handleListEvents returns the caller's recent activity history (tunnel
// creates, deletes, logins, etc.) newest first.
func (s *Server) handleListEvents(w http.ResponseWriter, r *http.Request) {
	claims, _ := claimsFrom(r.Context())
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}

	events, err := s.DB.RecentEvents(r.Context(), claims.UserID, limit)
	if err != nil {
		s.Log.Error("list events: db read failed", "err", err)
		writeErr(w, http.StatusInternalServerError, "could not list events")
		return
	}

	out := make([]map[string]any, 0, len(events))
	for _, e := range events {
		var payload any
		if len(e.Payload) > 0 {
			_ = json.Unmarshal(e.Payload, &payload)
		}
		out = append(out, map[string]any{
			"id":        e.ID,
			"kind":      e.Kind,
			"payload":   payload,
			"createdAt": e.CreatedAt,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": out})
}
