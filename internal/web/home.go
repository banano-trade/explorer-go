package web

import (
	"context"
	"math/big"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/banano-trade/explorer-go/internal/ban"
)

type feedItem struct {
	Hash, Type, Account, Name, Amount string
	Time                              int64
	ConfirmMS                         int64
}

type repShare struct {
	Account, Name string
	Weight        *big.Int
	Pct           float64
}

type homeData struct {
	Blocks, Accounts string
	OnlineReps       int
	OnlineWeight     *big.Int
	MedianMS, P95MS  int64
	Feed             []feedItem
	TopReps          []repShare
	Nakamoto         int
	Principal        int
	Circulating      *big.Int
	Total            *big.Int
	Dist             *big.Int
	CircPct          float64
	CPS              float64 // confirmations per second, last 10 minutes
	PriceUSD         float64
}

func (s *Server) home(w http.ResponseWriter, r *http.Request) {
	v, err := s.remember(r.Context(), "home", 10*time.Second, func(ctx context.Context) (any, error) { return s.loadHome(ctx) })
	if err != nil {
		s.nodeDown(w, r, err)
		return
	}
	d := *(v.(*homeData)) // per-request copy: price and supply change independently
	if sc := s.ledgerScan(); sc != nil && sc.Burned != nil {
		d.Total = new(big.Int).Sub(maxSupply, sc.Burned)
		d.Dist = s.distributionFunds(r.Context())
		d.Circulating = new(big.Int).Sub(d.Total, d.Dist)
		d.CircPct = ratio(d.Circulating, d.Total) * 100
	}
	d.PriceUSD = s.Prices.Get("usd")
	if snap := s.snapshot(r.Context()); snap != nil {
		d.Principal = snap.Principal
	}
	if s.Metrics != nil {
		pts := s.Metrics.Range(10 * time.Minute)
		var sum float64
		for _, p := range pts {
			sum += p.CPS
		}
		if len(pts) > 0 {
			d.CPS = sum / float64(len(pts))
		}
	}
	v = &d
	s.render(w, r, http.StatusOK, "home", page{Nav: "home", Description: "Look up any Banano account, block, alias or BNS name.", Data: v})
}

func (s *Server) loadHome(ctx context.Context) (*homeData, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	d := &homeData{}
	bc, err := s.Node.BlockCount(ctx)
	if err != nil {
		return nil, err
	}
	d.Blocks = ban.Group(bc.Count)
	var tel struct {
		AccountCount string `json:"account_count"`
	}
	if s.Node.Call(ctx, "telemetry", nil, &tel) == nil && tel.AccountCount != "" {
		d.Accounts = ban.Group(tel.AccountCount)
	}
	reps, sum, err := s.onlineReps(ctx)
	if err == nil {
		d.OnlineReps = len(reps)
		d.OnlineWeight = sum
		if len(reps) > 5 {
			d.TopReps = reps[:5]
		} else {
			d.TopReps = reps
		}
		d.Nakamoto = nakamoto(reps, sum)
	}
	if s.Live != nil {
		st := s.Live.Stats(time.Hour)
		if st.Count >= 20 {
			d.MedianMS, d.P95MS = st.Median, st.P95
		}
		for _, c := range s.Live.Latest(10, nil) {
			d.Feed = append(d.Feed, feedItem{Hash: c.Hash, Type: c.Type, Account: c.Account, Name: s.Known.Name(c.Account), Amount: c.Amount, Time: c.Time / 1000, ConfirmMS: c.DurationMS})
		}
	}
	if len(d.Feed) >= 10 {
		return d, nil
	}
	d.Feed = nil
	if ch, err := s.Node.ConfirmationHistory(ctx); err == nil {
		var ds []int64
		for _, c := range ch.Confirmations {
			ds = append(ds, atoi(c.Duration))
		}
		d.MedianMS, d.P95MS = percentiles(ds)
		// newest confirmations first
		sort.Slice(ch.Confirmations, func(i, j int) bool { return atoi(ch.Confirmations[i].Time) > atoi(ch.Confirmations[j].Time) })
		var hashes []string
		dur := map[string]int64{}
		for _, c := range ch.Confirmations {
			if len(hashes) == 10 {
				break
			}
			hashes = append(hashes, c.Hash)
			dur[c.Hash] = atoi(c.Duration)
		}
		if infos, err := s.Node.BlocksInfo(ctx, hashes); err == nil {
			for _, h := range hashes {
				b, ok := infos[h]
				if !ok || b.BlockAccount == "" {
					continue
				}
				d.Feed = append(d.Feed, feedItem{Hash: h, Type: blockType(b.Subtype, b.Contents.Type), Account: b.BlockAccount, Name: s.Known.Name(b.BlockAccount),
					Amount: b.Amount, Time: atoi(b.LocalTimestamp), ConfirmMS: dur[h]})
			}
		}
	}
	return d, nil
}

// onlineReps returns online reps sorted by weight with their share of online weight.
func (s *Server) onlineReps(ctx context.Context) ([]repShare, *big.Int, error) {
	v, err := s.remember(ctx, "onlineReps", 60*time.Second, func(ctx context.Context) (any, error) {
		m, err := s.Node.RepresentativesOnline(ctx)
		if err != nil {
			return nil, err
		}
		sum := new(big.Int)
		var out []repShare
		for a, w := range m {
			wi := ban.Raw(w)
			sum.Add(sum, wi)
			out = append(out, repShare{Account: a, Name: s.Known.Name(a), Weight: wi})
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Weight.Cmp(out[j].Weight) > 0 })
		for i := range out {
			out[i].Pct = ratio(out[i].Weight, sum) * 100
		}
		return [2]any{out, sum}, nil
	})
	if err != nil {
		return nil, nil, err
	}
	p := v.([2]any)
	return p[0].([]repShare), p[1].(*big.Int), nil
}

func ratio(a, b *big.Int) float64 {
	if b == nil || b.Sign() == 0 {
		return 0
	}
	f, _ := new(big.Float).Quo(new(big.Float).SetInt(a), new(big.Float).SetInt(b)).Float64()
	return f
}

// nakamoto counts the largest reps needed to pass 67% of online weight.
func nakamoto(reps []repShare, sum *big.Int) int {
	acc := 0.0
	for i, r := range reps {
		acc += r.Pct
		if acc > 67 {
			return i + 1
		}
	}
	return len(reps)
}

func percentiles(ds []int64) (median, p95 int64) {
	if len(ds) == 0 {
		return 0, 0
	}
	sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
	return ds[len(ds)/2], ds[(len(ds)*95)/100-1+boolInt(len(ds) < 20)]
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func blockType(subtype, typ string) string {
	switch subtype {
	case "send", "receive", "change", "open", "epoch":
		if subtype == "open" {
			return "receive"
		}
		return subtype
	}
	switch typ {
	case "send", "receive", "change", "open":
		if typ == "open" {
			return "receive"
		}
		return typ
	}
	return "state"
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
