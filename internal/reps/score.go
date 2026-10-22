package reps

import "fmt"

// Facts are what the score looks at for one representative.
type Facts struct {
	Online      bool
	UptimeMonth float64 // percent, -1 if unknown
	TrackedMins int     // how much uptime history there is
	SharePct    float64 // percent of online weight
	Principal   bool    // ≥0.1% of online weight; only principal reps' votes are relayed
	VotePct     float64 // percent of sampled blocks this rep voted on, -1 if no sample
	SharedHost  bool    // same IP as another rep's node
	HasNode     bool    // we matched telemetry to this rep
	Current     bool    // runs the newest node version
	InSync      bool    // fewer than 1,000 blocks behind the network
}

// Part is one line of the score breakdown.
type Part struct {
	Name   string
	Points int
	Max    int
	Why    string
}

// Score is a rep's grade with its breakdown.
type Score struct {
	Points int
	Grade  string // A, B, C, D or Offline
	Parts  []Part
}

// Rate scores a rep out of 100. Every rule is a plain threshold so anyone can
// check it; the representatives page lists them.
func Rate(f Facts) Score {
	var s Score
	add := func(p Part) { s.Parts = append(s.Parts, p); s.Points += p.Points }

	up := Part{Name: "Online", Max: 35}
	switch u := f.UptimeMonth; {
	case u < 0 || f.TrackedMins < 1440:
		// Too new to judge: half marks rather than punishing or rewarding.
		up.Points, up.Why = 18, "less than a day of history yet"
	case u >= 99.9:
		up.Points = 35
	case u >= 99.5:
		up.Points = 30
	case u >= 99:
		up.Points = 25
	case u >= 97:
		up.Points = 15
	case u >= 90:
		up.Points = 5
	}
	if up.Why == "" {
		up.Why = fmt.Sprintf("%.2f%% uptime over the last 30 days", f.UptimeMonth)
	}
	add(up)

	v := Part{Name: "Votes", Max: 25}
	switch {
	case !f.Principal:
		v.Points, v.Why = 15, "too small for its votes to be relayed, so not judged"
	case f.VotePct < 0:
		v.Points, v.Why = 15, "no blocks sampled yet"
	default:
		switch p := f.VotePct; {
		case p >= 95:
			v.Points = 25
		case p >= 80:
			v.Points = 18
		case p >= 50:
			v.Points = 10
		}
		v.Why = fmt.Sprintf("voted on %.0f%% of recent blocks", f.VotePct)
	}
	add(v)

	d := Part{Name: "Not too big", Max: 25}
	switch p := f.SharePct; {
	case p < 1:
		d.Points = 25
	case p < 3:
		d.Points = 20
	case p < 5:
		d.Points = 12
	case p < 10:
		d.Points = 5
	}
	d.Why = fmt.Sprintf("%.2f%% of online weight", f.SharePct)
	if f.SharedHost {
		d.Points = max(0, d.Points-8)
		d.Why += ", shares a server with another rep"
	}
	add(d)

	h := Part{Name: "Healthy node", Max: 15}
	switch {
	case !f.HasNode:
		h.Points, h.Why = 7, "node not visible in telemetry"
	default:
		if f.Current {
			h.Points += 8
			h.Why = "newest version"
		} else {
			h.Points += 3
			h.Why = "older version"
		}
		if f.InSync {
			h.Points += 7
			h.Why += ", in sync"
		} else {
			h.Why += ", behind the network"
		}
	}
	add(h)

	switch {
	case !f.Online:
		s.Grade = "Offline"
	case s.Points >= 85:
		s.Grade = "A"
	case s.Points >= 70:
		s.Grade = "B"
	case s.Points >= 50:
		s.Grade = "C"
	default:
		s.Grade = "D"
	}
	return s
}
