package web

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"math/big"
	"net/http"
	"strings"
	"time"

	"github.com/banano-trade/explorer-go/internal/ban"
	"github.com/banano-trade/explorer-go/internal/monitors"
	"github.com/banano-trade/explorer-go/internal/node"
	"github.com/banano-trade/explorer-go/internal/reps"
)

// snapshot returns the rep directory, refreshing it once if it's still empty.
func (s *Server) snapshot(ctx context.Context) *reps.Snapshot {
	if s.Reps == nil {
		return nil
	}
	// Never refresh from a request: the background job does that every minute.
	return s.Reps.Snapshot()
}

type segment struct {
	Name  string
	Pct   float64
	Color template.CSS
}

type repsData struct {
	*reps.Snapshot
	Rows     []*reps.Rep
	All      bool
	Segments []segment
	Groups   []string // sentences about reps sharing a node
	GradeA   int
}

var segColors = []template.CSS{"#1E5520", "#2B6E2C", "#338233", "#4CA94B", "#7CC77B", "#A9DCA8"}

func (s *Server) representatives(w http.ResponseWriter, r *http.Request) {
	snap := s.snapshot(r.Context())
	if snap == nil {
		s.repsPending(w, r)
		return
	}
	d := &repsData{Snapshot: snap, All: r.URL.Query().Get("all") == "1", GradeA: snap.CountGrade("A")}
	d.Rows = snap.Online
	// Only a few dozen reps are online, so list them all unless that ever
	// grows a lot; the filter box narrows the list.
	if !d.All && len(d.Rows) > 100 {
		d.Rows = d.Rows[:100]
	}
	rest := 100.0
	for i, rp := range snap.Online {
		if i == len(segColors) || rp.SharePct < 3 {
			break
		}
		d.Segments = append(d.Segments, segment{Name: repName(rp), Pct: rp.SharePct, Color: segColors[i]})
		rest -= rp.SharePct
	}
	if others := len(snap.Online) - len(d.Segments); others > 0 {
		d.Segments = append(d.Segments, segment{Name: fmt.Sprintf("%d others", others), Pct: max(rest, 0), Color: "#FBDD11"})
	}
	seen := map[string]bool{}
	for _, rp := range snap.Online {
		if len(rp.SharedWith) == 0 || seen[rp.Account] {
			continue
		}
		names := []string{repName(rp)}
		total := rp.SharePct
		seen[rp.Account] = true
		for _, o := range rp.SharedWith {
			if or := snap.By[o]; or != nil && !seen[o] {
				names = append(names, repName(or))
				total += or.SharePct
				seen[o] = true
			}
		}
		if len(names) > 1 {
			d.Groups = append(d.Groups, fmt.Sprintf("%s run on one node: together they hold %.1f%%.", joinNames(names), total))
		}
	}
	s.render(w, r, http.StatusOK, "representatives", page{Title: "Representatives", Nav: "reps", Description: "Banano representatives, their voting weight and rep score.", Data: d})
}

func (s *Server) delegate(w http.ResponseWriter, r *http.Request) {
	snap := s.snapshot(r.Context())
	if snap == nil {
		s.repsPending(w, r)
		return
	}
	filter := r.URL.Query().Get("grade")
	var rows []*reps.Rep
	for _, rp := range snap.Tracked {
		switch filter {
		case "a":
			if rp.Score.Grade != "A" {
				continue
			}
		case "avoid":
			if rp.Score.Grade != "D" && rp.Score.Grade != "Offline" {
				continue
			}
		}
		rows = append(rows, rp)
	}
	s.render(w, r, http.StatusOK, "delegate", page{Title: "Pick a good representative", Nav: "reps", Description: "Every Banano representative graded A to D for uptime, voting, size and node health.",
		Data: map[string]any{"Snap": snap, "Picks": snap.Picks(6), "Rows": rows, "Filter": filter}})
}

// delegateCheck takes any address and shows the card for its representative.
func (s *Server) delegateCheck(w http.ResponseWriter, r *http.Request) {
	q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("addr")))
	if _, err := ban.ParseAddress(q); err != nil {
		prob := "nomatch"
		if errors.Is(err, ban.ErrChecksum) {
			prob = "checksum"
		}
		s.render(w, r, http.StatusNotFound, "search", page{Title: "Address not valid", Data: searchData{Query: q, Problem: prob}})
		return
	}
	if snap := s.snapshot(r.Context()); snap != nil && snap.By[q] != nil {
		http.Redirect(w, r, "/rep/"+q, http.StatusFound)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	info, err := s.Node.AccountInfo(ctx, q)
	if errors.Is(err, node.ErrNotFound) {
		s.render(w, r, http.StatusNotFound, "search", page{Title: "Not opened", Data: searchData{Query: q, Problem: "unopened"}})
		return
	}
	if err != nil {
		s.nodeDown(w, r, err)
		return
	}
	http.Redirect(w, r, "/rep/"+info.Representative+"?mine="+q, http.StatusFound)
}

type repCardData struct {
	Monitor    monitors.Monitor
	Rep        *reps.Rep
	Snap       *reps.Snapshot
	Verdict    string
	Reason     string
	Days       []dayCell
	Delegators string
	Alts       []*reps.Rep
	Mine       string // the visitor's own account, when they came from "check my rep"
}

type dayCell struct {
	Color template.CSS
	Label string
}

func (s *Server) repCard(w http.ResponseWriter, r *http.Request) {
	addr := strings.ToLower(r.PathValue("addr"))
	if _, err := ban.ParseAddress(addr); err != nil {
		http.NotFound(w, r)
		return
	}
	snap := s.snapshot(r.Context())
	if snap == nil {
		s.repsPending(w, r)
		return
	}
	rp := snap.By[addr]
	if rp == nil {
		// Not a rep we list: tiny or never online. Their account page says enough.
		http.Redirect(w, r, "/account/"+addr, http.StatusFound)
		return
	}
	d := &repCardData{Rep: rp, Snap: snap}
	if m := r.URL.Query().Get("mine"); ban.LooksLikeAddress(m) {
		if _, err := ban.ParseAddress(m); err == nil {
			d.Mine = m
		}
	}
	d.Verdict, d.Reason = verdict(rp)
	d.Monitor, _ = s.Monitors.Get(addr)
	for _, p := range s.Reps.Uptime.Days(addr, 30) {
		d.Days = append(d.Days, dayColor(p))
	}
	if sc := s.ledgerScan(); sc != nil && sc.DelegatorCount[addr] > 0 {
		d.Delegators = fmt.Sprint(sc.DelegatorCount[addr])
	}
	for _, p := range snap.Picks(0) {
		if p.Account != addr && len(d.Alts) < 3 {
			d.Alts = append(d.Alts, p)
		}
	}
	s.render(w, r, http.StatusOK, "repcard", page{Title: repName(rp) + " rep score", Nav: "reps",
		Description: fmt.Sprintf("%s: grade %s, rep score %d/100.", repName(rp), rp.Score.Grade, rp.Score.Points), Data: d})
}

func dayColor(p float64) dayCell {
	switch {
	case p < 0:
		return dayCell{"var(--line2)", "not tracked"}
	case p >= 99.9:
		return dayCell{"#338233", fmt.Sprintf("%.1f%%", p)}
	case p >= 99:
		return dayCell{"#7CC77B", fmt.Sprintf("%.1f%%", p)}
	case p >= 90:
		return dayCell{"#E0A400", fmt.Sprintf("%.1f%%", p)}
	}
	return dayCell{"#C9611E", fmt.Sprintf("%.1f%%", p)}
}

// verdict is the one-line plain-language summary on the rep card.
func verdict(rp *reps.Rep) (string, string) {
	lead := map[string]string{"A": "Great pick.", "B": "Good choice.", "C": "Think twice.", "D": "Avoid for now.", "Offline": "Offline right now."}[rp.Score.Grade]
	var weak []string
	for _, p := range rp.Score.Parts {
		if p.Points*2 < p.Max {
			switch p.Name {
			case "Online":
				weak = append(weak, fmt.Sprintf("it was online only %.1f%% of the last 30 days", rp.Uptime.Month))
			case "Votes":
				weak = append(weak, fmt.Sprintf("it voted on only %.0f%% of recent blocks", rp.VotePct))
			case "Not too big":
				weak = append(weak, fmt.Sprintf("it already holds %.1f%% of online voting weight", rp.SharePct))
			case "Healthy node":
				weak = append(weak, "its node is out of date or behind")
			}
		}
	}
	switch {
	case !rp.Online:
		return lead, "Its weight doesn't count toward quorum while it's down. If you delegate to it, consider switching."
	case len(weak) == 0:
		return lead, "Reliable, votes, runs an up-to-date node, and isn't too big."
	}
	return lead, "Mainly because " + joinNames(weak) + "."
}

func repName(r *reps.Rep) string {
	if r.Name != "" {
		return r.Name
	}
	return ban.Short(r.Account)
}

func joinNames(n []string) string {
	switch len(n) {
	case 0:
		return ""
	case 1:
		return n[0]
	}
	return strings.Join(n[:len(n)-1], ", ") + " and " + n[len(n)-1]
}

// shortBan renders raw as "1.486B" / "309M" / "12,345".
func shortBan(raw *big.Int) string {
	if raw == nil {
		return "0"
	}
	f := ban.Float(raw)
	switch {
	case f >= 1e9:
		return fmt.Sprintf("%.3fB", f/1e9)
	case f >= 1e6:
		return fmt.Sprintf("%.0fM", f/1e6)
	}
	return ban.Group(fmt.Sprintf("%.0f", f))
}

// weakest is the short "why" on the score table: the part that lost most points.
func weakest(r *reps.Rep) string {
	if !r.Online {
		return "Offline now"
	}
	worst, lost := "", 0
	for _, p := range r.Score.Parts {
		if l := p.Max - p.Points; l > lost {
			worst, lost = p.Why, l
		}
	}
	if lost <= 3 {
		return "Solid all round"
	}
	return strings.ToUpper(worst[:1]) + worst[1:]
}

func (s *Server) repsPending(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Retry-After", "30")
	s.render(w, r, http.StatusOK, "pending", page{Title: "Representatives", Nav: "reps", Data: map[string]any{"What": "the representatives", "Why": "Rep scores refresh every minute; the first round finishes a few seconds after the explorer starts."}})
}
