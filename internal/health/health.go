// Package health decides whether the Banano network is confirming blocks
// normally (the WeGoodFam™ page). The rules are plain thresholds so anyone can
// check them; see evaluate.
package health

import (
	"context"
	"fmt"
	"log/slog"
	"math/big"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/banano-trade/explorer-go/internal/ban"
	"github.com/banano-trade/explorer-go/internal/live"
	"github.com/banano-trade/explorer-go/internal/node"
)

type Level int

const (
	Good Level = iota
	Note       // worth knowing, not a problem
	Warn       // something is off, blocks still confirm
	Bad        // borked: blocks aren't confirming
)

func (l Level) String() string {
	return [...]string{"good", "note", "warn", "bad"}[l]
}

// Check is one card on the health page.
type Check struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Level Level  `json:"-"`
	State string `json:"level"`
	Value string `json:"value"`
	What  string `json:"what"`
	Rule  string `json:"rule"`
	Link  string `json:"-"`
}

// Report is the result of one round of checks.
type Report struct {
	Good      bool      `json:"good"`
	NodeDown  bool      `json:"node_down"`
	CheckedAt time.Time `json:"checked_at"`
	Backlog   int64     `json:"backlog"`
	BadSince  time.Time `json:"backlog_since,omitzero"`
	Quorum    float64   `json:"quorum_ratio"`
	Headline  string    `json:"-"`
	Summary   string    `json:"summary"`
	Checks    []Check   `json:"checks"`
	Notes     []string  `json:"notes"`
	Problems  []string  `json:"problems"`
}

// Inputs is everything evaluate looks at.
type Inputs struct {
	Now             time.Time
	Count, Cemented int64
	Unchecked       int64
	BacklogSince    time.Time // when the backlog first reached 200, zero if it hasn't
	MedianMS        int64     // last hour; 0 if no samples
	Samples         int
	PeerCemented    int64 // highest cemented count among peers
	Online, Minimum *big.Int
	Delegated       *big.Int  // all delegated weight
	OnlineShares    []float64 // online reps' share of online weight, biggest first
	Peers           int
	LatestVersion   string
	OnLatest        float64 // share of official peers on LatestVersion
}

const (
	backlogLimit = 200
	backlogFor   = 15 * time.Minute
)

func pct(f float64) string { return strconv.FormatFloat(f*100, 'f', 1, 64) + "%" }

func evaluate(in Inputs) Report {
	r := Report{Good: true, CheckedAt: in.Now, Backlog: in.Count - in.Cemented, BadSince: in.BacklogSince}
	add := func(c Check) {
		c.State = c.Level.String()
		r.Checks = append(r.Checks, c)
		switch c.Level {
		case Bad:
			r.Good = false
			r.Problems = append(r.Problems, c.ID)
		case Warn:
			r.Problems = append(r.Problems, c.ID)
		case Note:
			r.Notes = append(r.Notes, c.ID)
		}
	}

	c := Check{ID: "backlog", Name: "Confirmations flowing", Link: "/blocks", Value: ban.Group(strconv.FormatInt(r.Backlog, 10)),
		What: "blocks waiting for confirmation on our node", Rule: "Borked if 200+ blocks stay unconfirmed for 15 minutes."}
	if r.Backlog >= backlogLimit {
		c.Level = Warn
		if !in.BacklogSince.IsZero() && in.Now.Sub(in.BacklogSince) >= backlogFor {
			c.Level = Bad
		}
	}
	add(c)

	c = Check{ID: "speed", Name: "Confirmation speed", Link: "/blocks", What: "median time from block to quorum, last hour", Rule: "Warns above 1 s, borked above 10 s."}
	if in.Samples == 0 {
		c.Value, c.What = "–", "no votes timed in the last hour"
	} else {
		c.Value = fmt.Sprintf("%d ms", in.MedianMS)
		switch {
		case in.MedianMS > 10000:
			c.Level = Bad
		case in.MedianMS > 1000:
			c.Level = Warn
		}
	}
	add(c)

	behind := in.PeerCemented - in.Cemented
	c = Check{ID: "sync", Name: "Our node is in sync", Link: "/network", Value: ban.Group(strconv.FormatInt(in.Cemented, 10)),
		What: "cemented blocks, same as the network", Rule: "Warns if we fall 1,000+ blocks behind peers' telemetry."}
	if behind >= 1000 {
		c.Level, c.What = Warn, fmt.Sprintf("cemented blocks, %s behind peers", ban.Group(strconv.FormatInt(behind, 10)))
	}
	add(c)

	c = Check{ID: "quorum", Name: "Quorum reachable", Link: "/network", Rule: "Borked below 100%: blocks can't reach quorum."}
	if in.Online != nil && in.Minimum != nil && in.Minimum.Sign() > 0 {
		r.Quorum = ratio(in.Online, in.Minimum)
		c.Value = fmt.Sprintf("%.0f%%", r.Quorum*100)
		c.What = fmt.Sprintf("%s BAN online vs. the %s minimum", short(in.Online), short(in.Minimum))
		if r.Quorum < 1 {
			c.Level = Bad
		}
	} else {
		c.Value, c.What, c.Level = "–", "couldn't read online weight", Warn
	}
	add(c)

	c = Check{ID: "online", Name: "Representatives online", Link: "/representatives", What: "of all delegated weight is online", Rule: "Warns below 67%."}
	if in.Online != nil && in.Delegated != nil && in.Delegated.Sign() > 0 {
		f := ratio(in.Online, in.Delegated)
		c.Value = pct(f)
		if f < 0.67 {
			c.Level = Warn
		}
	} else {
		c.Value = "–"
	}
	add(c)

	c = Check{ID: "peers", Name: "Peers", Link: "/network", Value: strconv.Itoa(in.Peers), What: "nodes connected to ours", Rule: "Warns below 10."}
	if in.Peers < 10 {
		c.Level = Warn
	}
	add(c)

	c = Check{ID: "unchecked", Name: "Unchecked blocks", Link: "/network", Value: ban.Group(strconv.FormatInt(in.Unchecked, 10)), What: "blocks our node can't place yet", Rule: "Warns above 1,000."}
	if in.Unchecked > 1000 {
		c.Level = Warn
	}
	add(c)

	nak := countTo(in.OnlineShares, 0.67)
	c = Check{ID: "nakamoto_coefficient", Name: "Nakamoto coefficient", Link: "/delegate", Value: strconv.Itoa(nak),
		What: "representatives could confirm blocks on their own", Rule: "A note below 10. Delegating to smaller reps helps."}
	if nak < 10 {
		c.Level = Note
	}
	add(c)

	stall := countTo(in.OnlineShares, 0.33)
	c = Check{ID: "stall_risk", Name: "Stall risk", Link: "/delegate", Value: strconv.Itoa(stall),
		What: "representatives going offline could stall the network", Rule: "A note below 4."}
	if stall < 4 {
		c.Level = Note
		var top float64
		for _, s := range in.OnlineShares[:min(stall, len(in.OnlineShares))] {
			top += s
		}
		c.Rule = fmt.Sprintf("A note below 4: the top %d hold %s of online weight.", stall, pct(top))
	}
	add(c)

	c = Check{ID: "versions", Name: "Node versions", Link: "/network", Rule: "Warns below 50%."}
	if in.LatestVersion != "" {
		c.Value, c.What = fmt.Sprintf("%.0f%%", in.OnLatest*100), "of peers run the latest V"+in.LatestVersion
		if in.OnLatest < 0.5 {
			c.Level = Warn
		}
	} else {
		c.Value, c.What = "–", "no telemetry from peers"
	}
	add(c)

	nn, nw := len(r.Notes), 0
	for _, ch := range r.Checks {
		if ch.Level == Warn {
			nw++
		}
	}
	switch {
	case !r.Good && r.Backlog >= backlogLimit:
		r.Headline = "Nope, borked."
		r.Summary = fmt.Sprintf("%s blocks have been waiting for confirmation for %d minutes. Payments sent now will confirm once the network recovers.",
			ban.Group(strconv.FormatInt(r.Backlog, 10)), int(in.Now.Sub(in.BacklogSince).Minutes()))
	case !r.Good:
		r.Headline = "Nope, borked."
		r.Summary = "Blocks aren't confirming normally right now. Payments sent now will confirm once the network recovers."
	case nw > 0:
		r.Headline = "We good, mostly."
		r.Summary = fmt.Sprintf("Banano is confirming blocks. %d of %d checks need a look.", nw, len(r.Checks))
	default:
		r.Headline = "We good, fam."
		r.Summary = fmt.Sprintf("Banano is confirming blocks normally. All %d checks pass", len(r.Checks))
		if nn > 0 {
			r.Summary += fmt.Sprintf(", %d with a note", nn)
		}
		r.Summary += "."
	}
	if r.Notes == nil {
		r.Notes = []string{}
	}
	if r.Problems == nil {
		r.Problems = []string{}
	}
	return r
}

// countTo is how many of the biggest shares it takes to pass limit.
func countTo(shares []float64, limit float64) int {
	acc := 0.0
	for i, s := range shares {
		acc += s
		if acc > limit {
			return i + 1
		}
	}
	return len(shares)
}

func ratio(a, b *big.Int) float64 {
	f, _ := new(big.Float).Quo(new(big.Float).SetInt(a), new(big.Float).SetInt(b)).Float64()
	return f
}

// short renders raw as "1.486B" or "900M".
func short(raw *big.Int) string {
	f := ban.Float(raw)
	switch {
	case f >= 1e9:
		return strconv.FormatFloat(f/1e9, 'f', 3, 64) + "B"
	case f >= 1e6:
		return strconv.FormatFloat(f/1e6, 'f', 0, 64) + "M"
	}
	return ban.Group(strconv.FormatFloat(f, 'f', 0, 64))
}

// Checker runs the checks every Interval and keeps the latest report.
type Checker struct {
	Node     *node.Client
	Live     *live.Feed // may be nil
	Log      *slog.Logger
	Interval time.Duration

	mu           sync.RWMutex
	last         Report
	backlogSince time.Time
	delegated    *big.Int
	delegatedAt  time.Time
}

func NewChecker(n *node.Client, l *live.Feed, log *slog.Logger) *Checker {
	return &Checker{Node: n, Live: l, Log: log, Interval: time.Minute}
}

// Last returns the latest report; ok is false before the first round.
func (c *Checker) Last() (Report, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.last, !c.last.CheckedAt.IsZero()
}

func (c *Checker) Run(ctx context.Context) {
	for {
		c.Once(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(c.Interval):
		}
	}
}

// Once runs one round of checks.
func (c *Checker) Once(ctx context.Context) Report {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	now := time.Now()
	in := Inputs{Now: now}
	bc, err := c.Node.BlockCount(ctx)
	if err != nil {
		r := Report{Good: false, NodeDown: true, CheckedAt: now, Headline: "Can't tell, our node is down.",
			Summary: "The explorer can't reach its Banano node, so it can't check the network right now.", Notes: []string{}, Problems: []string{"node_down"}}
		c.Log.Warn("health: node down", "err", err)
		c.store(r)
		return r
	}
	in.Count, in.Cemented, in.Unchecked = atoi(bc.Count), atoi(bc.Cemented), atoi(bc.Unchecked)

	c.mu.Lock()
	if in.Count-in.Cemented >= backlogLimit {
		if c.backlogSince.IsZero() {
			c.backlogSince = now
		}
	} else {
		c.backlogSince = time.Time{}
	}
	in.BacklogSince = c.backlogSince
	c.mu.Unlock()

	if c.Live != nil {
		st := c.Live.Stats(time.Hour)
		in.MedianMS, in.Samples = st.Median, st.Count
	}
	if q, err := c.Node.ConfirmationQuorum(ctx, false); err == nil {
		in.Online, in.Minimum = ban.Raw(q.OnlineStakeTotal), ban.Raw(q.OnlineWeightMinimum)
	}
	in.Delegated = c.delegatedWeight(ctx)
	if reps, err := c.Node.RepresentativesOnline(ctx); err == nil {
		sum := new(big.Int)
		ws := make([]*big.Int, 0, len(reps))
		for _, w := range reps {
			wi := ban.Raw(w)
			sum.Add(sum, wi)
			ws = append(ws, wi)
		}
		sort.Slice(ws, func(i, j int) bool { return ws[i].Cmp(ws[j]) > 0 })
		for _, w := range ws {
			in.OnlineShares = append(in.OnlineShares, ratio(w, sum))
		}
	}
	in.Peers, _ = c.Node.PeerCount(ctx)
	if tel, err := c.Node.TelemetryRaw(ctx); err == nil {
		versions := map[string]int{}
		official := 0
		for _, t := range tel {
			if v := atoi(t.CementedCount); v > in.PeerCemented {
				in.PeerCemented = v
			}
			if t.Maker != "0" { // other node implementations number their versions differently
				continue
			}
			official++
			versions[t.MajorVersion+"."+t.MinorVersion]++
		}
		best := ""
		for v := range versions {
			if best == "" || newer(v, best) {
				best = v
			}
		}
		if official > 0 {
			in.LatestVersion, in.OnLatest = best, float64(versions[best])/float64(official)
		}
	}
	r := evaluate(in)
	c.store(r)
	return r
}

func (c *Checker) store(r Report) {
	c.mu.Lock()
	c.last = r
	c.mu.Unlock()
}

// delegatedWeight sums every rep's weight; it changes slowly, so refresh hourly.
func (c *Checker) delegatedWeight(ctx context.Context) *big.Int {
	c.mu.RLock()
	d, at := c.delegated, c.delegatedAt
	c.mu.RUnlock()
	if d != nil && time.Since(at) < time.Hour {
		return d
	}
	reps, err := c.Node.Representatives(ctx)
	if err != nil {
		return d
	}
	sum := new(big.Int)
	for _, w := range reps {
		sum.Add(sum, ban.Raw(w))
	}
	c.mu.Lock()
	c.delegated, c.delegatedAt = sum, time.Now()
	c.mu.Unlock()
	return sum
}

// newer compares "26.1" style versions.
func newer(a, b string) bool {
	var am, an, bm, bn int
	fmt.Sscanf(a, "%d.%d", &am, &an)
	fmt.Sscanf(b, "%d.%d", &bm, &bn)
	return am > bm || (am == bm && an > bn)
}

func atoi(s string) int64 {
	n, _ := strconv.ParseInt(s, 10, 64)
	return n
}
