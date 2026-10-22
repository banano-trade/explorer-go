package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/banano-trade/explorer-go/internal/ban"
	"github.com/banano-trade/explorer-go/internal/known"
)

type searchData struct {
	Query   string
	Problem string // "", "checksum", "nomatch", "noblock", "nano", "down"
	Results []known.Account
}

// search sends exact addresses and hashes straight to their page and otherwise
// lists known accounts whose name matches.
func (s *Server) search(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		s.render(w, r, http.StatusOK, "search", page{Title: "Search", Nav: "search", Data: searchData{}})
		return
	}
	lq := strings.ToLower(q)
	if ban.LooksLikeAddress(lq) {
		if _, err := ban.ParseAddress(lq); err != nil {
			prob := "nomatch"
			if errors.Is(err, ban.ErrChecksum) {
				prob = "checksum"
			}
			s.render(w, r, http.StatusNotFound, "search", page{Title: "Search", Nav: "search", Data: searchData{Query: q, Problem: prob}})
			return
		}
		http.Redirect(w, r, "/account/"+lq, http.StatusFound)
		return
	}
	if strings.HasPrefix(lq, "nano_") || strings.HasPrefix(lq, "xrb_") {
		s.render(w, r, http.StatusNotFound, "search", page{Title: "Search", Nav: "search", Data: searchData{Query: q, Problem: "nano"}})
		return
	}
	if ban.IsHash(strings.ToUpper(q)) {
		http.Redirect(w, r, "/block/"+strings.ToUpper(q), http.StatusFound)
		return
	}
	res := s.Known.Search(q, 50)
	if len(res) == 1 && strings.EqualFold(res[0].Alias, q) {
		http.Redirect(w, r, "/account/"+res[0].Address, http.StatusFound)
		return
	}
	d := searchData{Query: q, Results: res}
	code := http.StatusOK
	if len(res) == 0 {
		d.Problem, code = "nomatch", http.StatusNotFound
	}
	s.render(w, r, code, "search", page{Title: "Search", Nav: "search", Data: d})
}

type suggestion struct {
	Name     string `json:"name"`
	Address  string `json:"address"`
	Short    string `json:"short"`
	Category string `json:"category"`
	URL      string `json:"url"`
}

// suggest powers the search-as-you-type list.
func (s *Server) suggest(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	out := []suggestion{}
	if len(q) >= 2 && len(q) <= 64 {
		for _, a := range s.Known.Search(q, 8) {
			out = append(out, suggestion{Name: a.Alias, Address: a.Address, Short: ban.Short(a.Address), Category: categoryLabel(a.Category), URL: "/account/" + a.Address})
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "public, max-age=300")
	_ = json.NewEncoder(w).Encode(out)
}

func categoryLabel(c string) string {
	switch c {
	case "representative":
		return "Rep"
	case "":
		return ""
	}
	return strings.ToUpper(c[:1]) + c[1:]
}

// nodeDown shows the friendly "can't reach our node" card.
func (s *Server) nodeDown(w http.ResponseWriter, r *http.Request, err error) {
	s.Log.Warn("node", "path", r.URL.Path, "err", err)
	w.Header().Set("Retry-After", "30")
	s.render(w, r, http.StatusServiceUnavailable, "search", page{Title: "Node unreachable", Data: searchData{Problem: "down"}})
}
