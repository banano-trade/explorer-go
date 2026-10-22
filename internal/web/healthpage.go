package web

import (
	"encoding/json"
	"net/http"
)

func (s *Server) healthPage(w http.ResponseWriter, r *http.Request) {
	if s.Health == nil {
		http.NotFound(w, r)
		return
	}
	rep, ok := s.Health.Last()
	if !ok {
		s.render(w, r, http.StatusOK, "pending", page{Title: "Network health", Nav: "health", Data: map[string]any{"What": "the health checks", "Why": "They run once a minute; the first round finishes a few seconds after the explorer starts."}})
		return
	}
	s.render(w, r, http.StatusOK, "health", page{Title: "Network health", Nav: "health", Description: rep.Summary, Data: rep})
}

// healthAPI lets bots and payment services pause themselves when the network stalls.
func (s *Server) healthAPI(w http.ResponseWriter, r *http.Request) {
	if s.Health == nil {
		http.NotFound(w, r)
		return
	}
	rep, ok := s.Health.Last()
	w.Header().Set("Content-Type", "application/json")
	if !ok {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"good":null,"pending":true}` + "\n"))
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(rep)
}
