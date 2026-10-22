package web

import (
	"context"
	"fmt"
	"html/template"
	"math/big"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/banano-trade/explorer-go/internal/ban"
	"github.com/banano-trade/explorer-go/internal/known"
	"github.com/banano-trade/explorer-go/internal/ledger"
	"github.com/banano-trade/explorer-go/internal/live"
	"github.com/banano-trade/explorer-go/internal/metrics"
)

// maxSupply is 2^128-1 raw, the amount created in the genesis block.
var maxSupply = new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 128), big.NewInt(1))

var ranges = map[string]time.Duration{"1d": 24 * time.Hour, "7d": 7 * 24 * time.Hour, "30d": 30 * 24 * time.Hour}

func pickRange(r *http.Request, key string) string {
	v := r.URL.Query().Get(key)
	if _, ok := ranges[v]; ok {
		return v
	}
	return "1d"
}

// teamBurners are Banano team addresses that burned, beyond the accounts the
// known list marks as Banano-owned (same list as burn.banano.trade).
var teamBurners = map[string]bool{
	"ban_1d59mzcc7yyuixyzc7femupc76yjsuoko79mm7y8td461opcpgiphjxjcje7": true,
	"ban_1fundm3d7zritekc8bdt4oto5ut8begz6jnnt7n3tdxzjq3t46aiuse1h7gj": true,
	"ban_1meshhme6ntahc4dzteguy364mxrw3smkgn4w3a3bqgcrm9q1wc47udpf4iy": true,
	"ban_3de1i3tq688qdjzf5f8bhmed3utja9d1oqwsnchnct1gyacsftfn7nycx95t": true,
	"ban_1b1ack1188caohzjdj65uarnk4kobzrnr3q3oc3bew5rfkyqxzu81zhjgp1e": true,
	"ban_3eg7hsqtt84sr6fyfgpemazhqdj5gnir7q7gxrmt4mozndehnt6un73y51u9": true,
	"ban_1h9n6con98taxzp64ozu4u6k1y9171kp7j5e89tgdewbwa5az6aje1znpt7t": true,
	"ban_3iejwmk1n3fqdntwcgudhmddo9bpwa8jzx6g361iq6rzbsrzonekmdus9yj5": true,
	"ban_3jb1fp4diu79wggp7e171jdpxp95auji4moste6gmc55pptwerfjqu48okse": true,
	"ban_3t4uj4yqgu4ipsmtqkg6xo3ogeitumyxcix88zga73h8ktn5opygum6hepdo": true,
	"ban_1forma18xs59rrz17akie17ne3guqph5nqf87fgg8gcd3jx81ty8poeh4z4c": true,
	"ban_3o696jra8n96s6jdtjm6fst9jyd58aahtqaex1m86j3hdimegm397zfxxghw": true,
}

func (s *Server) isTeam(addr string) bool { return teamBurners[addr] || s.Known.IsBanano(addr) }

type supplyRow struct {
	Name  string
	Color template.CSS
	Raw   *big.Int
	Pct   float64
}

type versionRow struct {
	Name string
	N    int
	Pct  float64
}

type networkData struct {
	At                    time.Time
	Nakamoto, Stall       int
	StallNames            string
	Speed                 string
	SpeedMedian, SpeedAvg int64
	SpeedP95              int64
	Voters                int
	SpeedChart            template.HTML
	Chart, ChartRange     string
	Charts                []struct{ Key, Name string }
	HistoryChart          template.HTML
	Supply, Weight        []supplyRow
	Total, Burned         *big.Int
	OnlineWeight, Minimum *big.Int
	QuorumDelta           *big.Int
	QuorumPct             string
	Versions              []versionRow
	Reporting             int
}

func (s *Server) network(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	d := &networkData{At: time.Now(), Speed: pickRange(r, "speed"), ChartRange: pickRange(r, "range"), Chart: r.URL.Query().Get("chart")}
	for _, sr := range metrics.Series {
		d.Charts = append(d.Charts, struct{ Key, Name string }{sr.Key, sr.Name})
	}
	valid := false
	for _, c := range d.Charts {
		valid = valid || c.Key == d.Chart
	}
	if !valid {
		d.Chart = "cps"
	}

	if snap := s.snapshot(ctx); snap != nil {
		d.Nakamoto = snap.Nakamoto
		acc := 0.0
		var names []string
		for _, rp := range snap.Online {
			acc += rp.SharePct
			names = append(names, repName(rp))
			if acc > 33 {
				break
			}
		}
		d.Stall, d.StallNames = len(names), joinNames(names)
	}

	// Confirmation speed: recent blocks for a day, hourly stats beyond.
	if s.Live != nil {
		dur := ranges[d.Speed]
		st := s.Live.Stats(dur)
		d.SpeedMedian, d.SpeedAvg, d.SpeedP95, d.Voters = st.Median, st.Avg, st.P95, st.Voters
		var xs []int64
		var med, p95 []float64
		cut := time.Now().Add(-dur).Unix()
		var wsum, msum, psum float64
		if d.Speed == "1d" {
			// Hourly records may not exist yet; 15-minute buckets from recent blocks.
			xs, med, p95 = recentBuckets(s.Live.Latest(5000, nil), 15*time.Minute, cut)
		}
		for _, h := range s.Live.Hours() {
			if d.Speed == "1d" && len(xs) > 0 {
				break
			}
			if h.Start < cut {
				continue
			}
			xs = append(xs, h.Start)
			med = append(med, float64(h.Median))
			p95 = append(p95, float64(h.P95))
			wsum += float64(h.Count)
			msum += float64(h.Median) * float64(h.Count)
			psum += float64(h.P95) * float64(h.Count)
		}
		if d.Speed != "1d" && wsum > 0 { // recent blocks don't reach back a week; use the hourly record
			d.SpeedMedian, d.SpeedP95, d.SpeedAvg = int64(msum/wsum), int64(psum/wsum), 0
		}
		d.SpeedChart = lineChart("Confirmation time, median and 95th percentile", " ms", []Line{
			{Name: "Median", Color: "var(--c1)", X: xs, Y: med},
			{Name: "95% within", Color: "var(--c2)", Dash: true, X: xs, Y: p95},
		})
	}
	if s.Metrics != nil {
		pts := s.Metrics.Range(ranges[d.ChartRange])
		var xs []int64
		var ys []float64
		name, unit := "", ""
		for _, sr := range metrics.Series {
			if sr.Key == d.Chart {
				name, unit = sr.Name, sr.Unit
			}
		}
		for _, p := range pts {
			xs = append(xs, p.T)
			ys = append(ys, p.Get(d.Chart))
		}
		d.HistoryChart = lineChart(name, unit, []Line{{Name: name, Color: "var(--c1)", X: xs, Y: ys}})
	}

	// Supply.
	d.Burned = new(big.Int)
	if sc := s.ledgerScan(); sc != nil && sc.Burned != nil {
		d.Burned = sc.Burned
	}
	d.Total = new(big.Int).Sub(maxSupply, d.Burned)
	dist := s.distributionFunds(ctx)
	circ := new(big.Int).Sub(d.Total, dist)
	d.Supply = []supplyRow{
		{Name: "Circulating", Color: "var(--jungle)", Raw: circ, Pct: ratio(circ, d.Total) * 100},
		{Name: "Distribution funds", Color: "var(--yellow)", Raw: dist, Pct: ratio(dist, d.Total) * 100},
	}

	// Voting weight and quorum.
	if q, err := s.Node.ConfirmationQuorum(ctx, false); err == nil {
		d.OnlineWeight, d.Minimum, d.QuorumDelta, d.QuorumPct = ban.Raw(q.OnlineStakeTotal), ban.Raw(q.OnlineWeightMinimum), ban.Raw(q.QuorumDelta), q.OnlineWeightQuorumPct
	}
	if snap := s.snapshot(ctx); snap != nil && d.OnlineWeight != nil {
		none := new(big.Int).Sub(d.Total, d.OnlineWeight)
		none.Sub(none, snap.OfflineWeight)
		if none.Sign() < 0 {
			none.SetInt64(0)
		}
		d.Weight = []supplyRow{
			{Name: "Online reps", Color: "var(--jungle)", Raw: d.OnlineWeight, Pct: ratio(d.OnlineWeight, d.Total) * 100},
			{Name: "Offline reps", Color: "#9C9EA6", Raw: snap.OfflineWeight, Pct: ratio(snap.OfflineWeight, d.Total) * 100},
			{Name: "No representative", Color: "var(--yellow)", Raw: none, Pct: ratio(none, d.Total) * 100},
		}
	}

	// Node versions from telemetry.
	if tel, err := s.Node.TelemetryRaw(ctx); err == nil {
		count := map[string]int{}
		for _, t := range tel {
			name := "V" + t.MajorVersion + "." + t.MinorVersion
			if t.Maker != "0" {
				name = "Other"
			}
			count[name]++
		}
		d.Reporting = len(tel)
		for n, c := range count {
			d.Versions = append(d.Versions, versionRow{Name: n, N: c, Pct: 100 * float64(c) / float64(len(tel))})
		}
		sort.Slice(d.Versions, func(i, j int) bool {
			if d.Versions[i].N != d.Versions[j].N {
				return d.Versions[i].N > d.Versions[j].N
			}
			return d.Versions[i].Name > d.Versions[j].Name
		})
	}
	s.render(w, r, http.StatusOK, "network", page{Title: "Network", Nav: "network", Description: "Supply, voting weight and health of the Banano network.", Data: d})
}

func (s *Server) ledgerScan() *ledger.Scan {
	if s.Ledger == nil {
		return nil
	}
	return s.Ledger.Last()
}

// distributionFunds sums the balances of the Banano distribution accounts.
func (s *Server) distributionFunds(ctx context.Context) *big.Int {
	v, err := s.remember(ctx, "distfunds", 10*time.Minute, func(ctx context.Context) (any, error) {
		sum := new(big.Int)
		for _, a := range s.Known.All() {
			if a.Category != "distribution" {
				continue
			}
			var b struct {
				Balance    string `json:"balance"`
				Receivable string `json:"receivable"`
			}
			if err := s.Node.Call(ctx, "account_balance", map[string]any{"account": a.Address}, &b); err != nil {
				return nil, err
			}
			sum.Add(sum, ban.Raw(b.Balance))
			sum.Add(sum, ban.Raw(b.Receivable))
		}
		return sum, nil
	})
	if err != nil {
		return new(big.Int)
	}
	return v.(*big.Int)
}

type richRow struct {
	ledger.Holder
	Name, Category string
	Team, Exchange bool
	Pct            float64
	Idle           bool // no activity for a year
}

func (s *Server) richlist(w http.ResponseWriter, r *http.Request) {
	sc := s.ledgerScan()
	if sc == nil {
		s.render(w, r, http.StatusOK, "pending", page{Title: "Rich list", Nav: "rich", Data: map[string]any{"What": "the rich list", "Why": "It comes from a full scan of the ledger, which runs when the explorer starts and then every hour. Try again in a minute."}})
		return
	}
	q := r.URL.Query()
	hideTeam, hideEx := q.Get("team") == "hide", q.Get("ex") == "hide"
	limit := 50
	if q.Get("n") == "500" {
		limit = 500
	}
	total := new(big.Int).Sub(maxSupply, sc.Burned)
	circ := new(big.Int).Sub(total, s.distributionFunds(r.Context()))
	yearAgo := time.Now().AddDate(-1, 0, 0).Unix()
	var rows []richRow
	top10, top100, team := new(big.Int), new(big.Int), new(big.Int)
	teamN := 0
	for i, h := range sc.Top {
		k, _ := s.Known.Get(h.Account)
		row := richRow{Holder: h, Name: k.Alias, Category: k.Category, Team: s.isTeam(h.Account), Exchange: k.Category == "exchange",
			Pct: ratio(h.Balance, circ) * 100, Idle: h.Modified > 0 && h.Modified < yearAgo}
		if i < 10 {
			top10.Add(top10, h.Balance)
		}
		if i < 100 {
			top100.Add(top100, h.Balance)
			if row.Team {
				team.Add(team, h.Balance)
				teamN++
			}
		}
		if (hideTeam && row.Team) || (hideEx && row.Exchange) || len(rows) >= limit {
			continue
		}
		rows = append(rows, row)
	}
	maxBucket := 1
	for _, b := range sc.Buckets {
		maxBucket = max(maxBucket, b.Count)
	}
	movers := sc.Movers24h
	mv := q.Get("movers")
	if mv == "7d" {
		movers = sc.Movers7d
	} else {
		mv = "24h"
	}
	type moverRow struct {
		ledger.Mover
		Name string
	}
	var mrows []moverRow
	for _, m := range movers {
		if len(mrows) == 8 {
			break
		}
		mrows = append(mrows, moverRow{m, s.Known.Name(m.Account)})
	}
	s.render(w, r, http.StatusOK, "richlist", page{Title: "Rich list", Nav: "rich", Description: "The biggest Banano balances, with known names.", Data: map[string]any{
		"Scan": sc, "Rows": rows, "HideTeam": hideTeam, "HideEx": hideEx, "Limit": limit,
		"Top10": ratio(top10, circ) * 100, "Top100": ratio(top100, circ) * 100, "Top100Raw": top100, "TeamN": teamN, "TeamRaw": team,
		"MaxBucket": maxBucket, "Movers": mrows, "MoversRange": mv,
	}})
}

type burnRow struct {
	ledger.Burner
	Name  string
	Share float64
}

func (s *Server) burn(w http.ResponseWriter, r *http.Request) {
	sc := s.ledgerScan()
	if sc == nil || sc.Burned == nil {
		s.render(w, r, http.StatusOK, "pending", page{Title: "Burn", Nav: "burn", Data: map[string]any{"What": "the burn leaderboard", "Why": "It's counted during the explorer's ledger scan, which runs at startup and then every hour. Try again in a minute."}})
		return
	}
	who := r.URL.Query().Get("who")
	switch who {
	case "team", "all":
	default:
		who = "community"
	}
	commRaw, teamRaw := new(big.Int), new(big.Int)
	commN, teamN := 0, 0
	for _, b := range sc.Burners {
		if s.isTeam(b.Account) {
			teamRaw.Add(teamRaw, b.Amount)
			teamN++
		} else {
			commRaw.Add(commRaw, b.Amount)
			commN++
		}
	}
	base := commRaw
	switch who {
	case "team":
		base = teamRaw
	case "all":
		base = sc.Burned
	}
	limit := 25
	if r.URL.Query().Get("n") == "100" {
		limit = 100
	}
	var rows []burnRow
	shown := 0
	for _, b := range sc.Burners {
		team := s.isTeam(b.Account)
		if (who == "community" && team) || (who == "team" && !team) {
			continue
		}
		shown++
		if len(rows) < limit {
			rows = append(rows, burnRow{Burner: b, Name: s.Known.Name(b.Account), Share: ratio(b.Amount, base) * 100})
		}
	}
	total := new(big.Int).Sub(maxSupply, sc.Burned)
	burnAddr := "ban_1burnbabyburndiscoinferno111111111111111111111111111aj49sw3w"
	s.render(w, r, http.StatusOK, "burn", page{Title: "Burned BAN", Nav: "burn", Description: fmt.Sprintf("%s BAN burned forever.", ban.Group(fmt.Sprintf("%.0f", ban.Float(sc.Burned)))), Data: map[string]any{
		"Scan": sc, "Rows": rows, "Who": who, "Shown": shown, "Limit": limit,
		"BurnedPct": ratio(sc.Burned, maxSupply) * 100, "Left": total,
		"CommRaw": commRaw, "CommN": commN, "TeamRaw": teamRaw, "TeamN": teamN, "CommPct": ratio(commRaw, sc.Burned) * 100,
		"Addr": burnAddr, "QR": qrSVG("ban:" + burnAddr),
	}})
}

type knownRow struct {
	known.Account
	Balance *big.Int
}

func (s *Server) knownPage(w http.ResponseWriter, r *http.Request) {
	cat := r.URL.Query().Get("cat")
	sortBy := r.URL.Query().Get("sort")
	counts := s.Known.Counts()
	var cats []string
	for c := range counts {
		cats = append(cats, c)
	}
	sort.Slice(cats, func(i, j int) bool { return counts[cats[i]] > counts[cats[j]] })
	if _, ok := counts[cat]; !ok {
		cat = ""
	}
	sc := s.ledgerScan()
	var rows []knownRow
	for _, a := range s.Known.All() {
		if cat != "" && a.Category != cat {
			continue
		}
		row := knownRow{Account: a}
		if sc != nil {
			row.Balance = sc.Balance[a.Address]
		}
		rows = append(rows, row)
	}
	switch sortBy {
	case "balance":
		sort.SliceStable(rows, func(i, j int) bool {
			bi, bj := rows[i].Balance, rows[j].Balance
			if bi == nil || bj == nil {
				return bi != nil
			}
			return bi.Cmp(bj) > 0
		})
	case "name":
		sort.SliceStable(rows, func(i, j int) bool { return strings.ToLower(rows[i].Alias) < strings.ToLower(rows[j].Alias) })
	default:
		sortBy = "category"
	}
	s.render(w, r, http.StatusOK, "known", page{Title: "Known accounts", Nav: "known", Description: "Exchanges, faucets, representatives and other Banano accounts with a public name.", Data: map[string]any{
		"Rows": rows, "Cats": cats, "Counts": counts, "Cat": cat, "Sort": sortBy, "Total": len(s.Known.All()), "Scanned": sc != nil,
	}})
}

// recentBuckets groups confirmations (newest first) into buckets of size and
// returns each bucket's median and 95th percentile confirm time.
func recentBuckets(cs []live.Confirmation, size time.Duration, cut int64) ([]int64, []float64, []float64) {
	by := map[int64][]int64{}
	for _, c := range cs {
		t := c.Time / 1000
		if t < cut || c.DurationMS <= 0 {
			continue
		}
		b := t - t%int64(size.Seconds())
		by[b] = append(by[b], c.DurationMS)
	}
	keys := make([]int64, 0, len(by))
	for k := range by {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	var xs []int64
	var med, p95 []float64
	for _, k := range keys {
		m, p, _ := live.Percentiles(by[k])
		xs = append(xs, k)
		med = append(med, float64(m))
		p95 = append(p95, float64(p))
	}
	return xs, med, p95
}
