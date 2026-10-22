package reps

import (
	"context"
	"log/slog"
	"math/big"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/banano-trade/explorer-go/internal/ban"
	"github.com/banano-trade/explorer-go/internal/live"
	"github.com/banano-trade/explorer-go/internal/node"
)

// Rep is everything the pages show about one representative.
type Rep struct {
	Account    string
	Name       string
	Weight     *big.Int
	SharePct   float64 // of online weight
	Online     bool
	Principal  bool
	Uptime     Stats
	VotePct    float64 // -1 when there's no sample
	HasNode    bool
	Version    string // "26.1"
	Lag        int64  // blocks behind the most cemented peer
	Peers      int
	Host       string   // node IP
	SharedWith []string // other reps on the same IP
	Score      Score
}

// Snapshot is one refresh of the whole directory.
type Snapshot struct {
	At            time.Time
	Online        []*Rep // by weight, biggest first
	Offline       []*Rep // largest offline reps worth listing
	Tracked       []*Rep // online plus tracked offline, by score
	By            map[string]*Rep
	OnlineWeight  *big.Int
	OfflineWeight *big.Int // delegated to reps that aren't online
	Minimum       *big.Int // online weight needed for quorum
	Principal     int
	Nakamoto      int
	LatestVersion string
	VoteBlocks    int // blocks in the vote sample

	// Quorum margin: how much of the online weight actually votes, and how
	// far that is above the 67% blocks need.
	VotingPct  float64 // share of online weight held by reps that vote
	Silent     []*Rep  // principal reps counted online that voted on under half the sampled blocks
	MarginPct  float64 // VotingPct - 67
	StallCount int     // fewest voting reps whose going offline drops voting weight below 67%
	StallReps  []*Rep
}

// Quorum is the share of online weight needed to confirm a block.
const Quorum = 67.0

// Namer looks up public names for accounts.
type Namer func(addr string) string

// Directory keeps a fresh Snapshot.
type Directory struct {
	Node   *node.Client
	Live   *live.Feed // may be nil: votes then count as unknown
	Uptime *Uptime
	Name   Namer
	Log    *slog.Logger
	// OwnReps are representatives voting from the explorer's own node; peer
	// telemetry never includes that node, so they use its local telemetry.
	OwnReps []string

	mu   sync.RWMutex
	snap *Snapshot
}

var (
	oneBan      = new(big.Int).Exp(big.NewInt(10), big.NewInt(29), nil)
	listOffline = new(big.Int).Mul(big.NewInt(100000), oneBan) // offline reps shown on the page
)

// Snapshot returns the latest refresh, or nil before the first one.
func (d *Directory) Snapshot() *Snapshot {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.snap
}

// Run refreshes every minute (which is also the uptime resolution).
func (d *Directory) Run(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	saves := 0
	for {
		if err := d.Refresh(ctx, true); err != nil {
			d.Log.Warn("reps refresh", "err", err)
		}
		if saves++; saves%5 == 0 {
			if err := d.Uptime.Save(); err != nil {
				d.Log.Warn("uptime save", "err", err)
			}
		}
		select {
		case <-ctx.Done():
			if err := d.Uptime.Save(); err != nil {
				d.Log.Warn("uptime save", "err", err)
			}
			return
		case <-t.C:
		}
	}
}

// Refresh rebuilds the snapshot. record says whether this counts as an uptime check.
func (d *Directory) Refresh(ctx context.Context, record bool) error {
	ctx, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()
	now := time.Now()
	online, err := d.Node.RepresentativesOnline(ctx)
	if err != nil {
		return err
	}
	all, err := d.Node.Representatives(ctx)
	if err != nil {
		return err
	}
	q, _ := d.Node.ConfirmationQuorum(ctx, true)
	tel, _ := d.Node.TelemetryRaw(ctx)

	if record {
		on := make(map[string]bool, len(online))
		for a := range online {
			on[a] = true
		}
		d.Uptime.Record(now, on, on)
	}

	s := &Snapshot{At: now, By: map[string]*Rep{}, OnlineWeight: new(big.Int), OfflineWeight: new(big.Int), Minimum: ban.Raw(q.OnlineWeightMinimum)}
	for a, w := range online {
		r := &Rep{Account: a, Weight: ban.Raw(w), Online: true}
		if aw, ok := all[a]; ok { // the online list can lag; delegated weight is current
			r.Weight = ban.Raw(aw)
		}
		s.OnlineWeight.Add(s.OnlineWeight, r.Weight)
		s.Online = append(s.Online, r)
		s.By[a] = r
	}
	if qo := ban.Raw(q.OnlineStakeTotal); qo.Sign() > 0 {
		s.OnlineWeight = qo // the node's own figure, what quorum is based on
	}
	for a, w := range all {
		if s.By[a] != nil {
			continue
		}
		wi := ban.Raw(w)
		s.OfflineWeight.Add(s.OfflineWeight, wi)
		if wi.Cmp(listOffline) >= 0 || d.Uptime.Stats(a).Tracked {
			r := &Rep{Account: a, Weight: wi}
			s.Offline = append(s.Offline, r)
			s.By[a] = r
		}
	}

	// Node details: the quorum peer list maps rep -> IP:port, telemetry maps IP:port -> node.
	type nodeInfo struct {
		version  string
		cemented int64
		peers    int
		maker    string
	}
	nodes := map[string]nodeInfo{}
	var maxCemented int64
	versions := map[string]int{}
	for _, t := range tel {
		c, _ := strconv.ParseInt(t.CementedCount, 10, 64)
		maxCemented = max(maxCemented, c)
		p, _ := strconv.Atoi(t.PeerCount)
		v := t.MajorVersion + "." + t.MinorVersion
		nodes[net.JoinHostPort(normIP(t.Address), t.Port)] = nodeInfo{version: v, cemented: c, peers: p, maker: t.Maker}
		if t.Maker == "0" {
			versions[v]++
		}
	}
	for v := range versions {
		if s.LatestVersion == "" || newerVersion(v, s.LatestVersion) {
			s.LatestVersion = v
		}
	}
	hosts := map[string][]string{}
	for _, p := range q.Peers {
		r := s.By[p.Account]
		if r == nil {
			continue
		}
		host, port, err := net.SplitHostPort(p.IP)
		if err != nil {
			continue
		}
		r.Host = normIP(host)
		hosts[r.Host] = append(hosts[r.Host], r.Account)
		if n, ok := nodes[net.JoinHostPort(r.Host, port)]; ok {
			r.HasNode, r.Version, r.Lag, r.Peers = true, n.version, maxCemented-n.cemented, n.peers
		}
	}
	if len(d.OwnReps) > 0 {
		if t, err := d.Node.TelemetryLocal(ctx); err == nil {
			c, _ := strconv.ParseInt(t.CementedCount, 10, 64)
			p, _ := strconv.Atoi(t.PeerCount)
			for _, a := range d.OwnReps {
				if r := s.By[a]; r != nil {
					r.HasNode, r.Version, r.Lag, r.Peers = true, t.MajorVersion+"."+t.MinorVersion, max(0, maxCemented-c), p
				}
			}
		}
	}
	for _, accts := range hosts {
		if len(accts) < 2 {
			continue
		}
		for _, a := range accts {
			for _, b := range accts {
				if a != b {
					s.By[a].SharedWith = append(s.By[a].SharedWith, b)
				}
			}
		}
	}

	var voteBlocks int
	var votes map[string]int
	if d.Live != nil {
		voteBlocks, votes = d.Live.VoteShare()
	}
	s.VoteBlocks = voteBlocks

	for _, r := range s.By {
		r.Name = d.Name(r.Account)
		r.SharePct = ratio(r.Weight, s.OnlineWeight) * 100
		r.Principal = r.SharePct >= 0.1
		if r.Online && r.Principal {
			s.Principal++
		}
		r.Uptime = d.Uptime.Stats(r.Account)
		r.VotePct = -1
		if voteBlocks >= 20 { // too few blocks says nothing
			r.VotePct = 100 * float64(votes[r.Account]) / float64(voteBlocks)
		}
		r.Score = Rate(Facts{
			Online: r.Online, UptimeMonth: r.Uptime.Month, TrackedMins: r.Uptime.TrackedMins,
			SharePct: r.SharePct, Principal: r.Principal, VotePct: r.VotePct, SharedHost: len(r.SharedWith) > 0,
			HasNode: r.HasNode, Current: r.Version == s.LatestVersion, InSync: r.Lag < 1000,
		})
	}
	byWeight := func(l []*Rep) {
		sort.Slice(l, func(i, j int) bool { return l[i].Weight.Cmp(l[j].Weight) > 0 })
	}
	byWeight(s.Online)
	byWeight(s.Offline)
	acc := 0.0
	for i, r := range s.Online {
		acc += r.SharePct
		if acc > 67 {
			s.Nakamoto = i + 1
			break
		}
	}
	// Reps whose votes we sampled and that skipped most blocks don't help reach
	// quorum even though the node lists them as online. Unknown = assume voting.
	var voting []*Rep
	for _, r := range s.Online {
		if r.Principal && r.VotePct >= 0 && r.VotePct < 50 {
			s.Silent = append(s.Silent, r)
			continue
		}
		s.VotingPct += r.SharePct
		voting = append(voting, r)
	}
	s.MarginPct = s.VotingPct - Quorum
	left := s.VotingPct
	for _, r := range voting { // biggest first
		if left < Quorum {
			break
		}
		left -= r.SharePct
		s.StallReps = append(s.StallReps, r)
	}
	s.StallCount = len(s.StallReps)
	for _, r := range s.By {
		if r.Online || r.Uptime.Tracked {
			s.Tracked = append(s.Tracked, r)
		}
	}
	sort.Slice(s.Tracked, func(i, j int) bool {
		a, b := s.Tracked[i], s.Tracked[j]
		if a.Score.Points != b.Score.Points {
			return a.Score.Points > b.Score.Points
		}
		return a.Weight.Cmp(b.Weight) > 0
	})

	d.mu.Lock()
	d.snap = s
	d.mu.Unlock()
	return nil
}

// Picks are A-grade reps under 1% of weight, the ones worth delegating to.
func (s *Snapshot) Picks(n int) []*Rep {
	var out []*Rep
	for _, r := range s.Tracked {
		if r.Online && r.Score.Grade == "A" && r.SharePct < 1 {
			out = append(out, r)
		}
	}
	if n > 0 && len(out) > n {
		out = out[:n]
	}
	return out
}

// CountGrade counts online reps with grade g.
func (s *Snapshot) CountGrade(g string) int {
	n := 0
	for _, r := range s.Online {
		if r.Score.Grade == g {
			n++
		}
	}
	return n
}

func normIP(ip string) string {
	ip = strings.Trim(ip, "[]")
	return strings.TrimPrefix(ip, "::ffff:")
}

func ratio(a, b *big.Int) float64 {
	if b == nil || b.Sign() == 0 {
		return 0
	}
	f, _ := new(big.Float).Quo(new(big.Float).SetInt(a), new(big.Float).SetInt(b)).Float64()
	return f
}

func newerVersion(a, b string) bool {
	pa, pb := strings.SplitN(a, ".", 2), strings.SplitN(b, ".", 2)
	am, _ := strconv.Atoi(pa[0])
	bm, _ := strconv.Atoi(pb[0])
	if am != bm {
		return am > bm
	}
	an, bn := 0, 0
	if len(pa) > 1 {
		an, _ = strconv.Atoi(pa[1])
	}
	if len(pb) > 1 {
		bn, _ = strconv.Atoi(pb[1])
	}
	return an > bn
}
