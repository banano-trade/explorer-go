package web

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/banano-trade/explorer-go/internal/ban"
	"github.com/banano-trade/explorer-go/internal/insights"
	"github.com/banano-trade/explorer-go/internal/known"
	"github.com/banano-trade/explorer-go/internal/node"
	"github.com/banano-trade/explorer-go/internal/reps"
)

const pageSize = 50

type txRow struct {
	Height, Type, Hash, Party, PartyName, Amount string
	Time                                         int64
	Pending                                      bool
}

type rcvRow struct {
	Hash, From, FromName, Amount string
	Time                         int64
}

type dgRow struct {
	Account, Name, Balance string
	Pct                    float64
}

// oneBan in raw; smaller delegators are left out of the delegators list.
const oneBan = "100000000000000000000000000000"

type dgList struct {
	Rows  []dgRow
	Total int
}

type accountData struct {
	InsChart             template.HTML
	DgPending            bool
	JumpFailed           bool
	Filter               string // transaction type filter
	Scanned              int
	FromHeight, ToHeight string
	RcvCount             string
	Ins                  *insights.Result
	InsMax               int64
	InsYears             []yearRow
	InsNote              string // plain-language headline, e.g. pass-through
	InsNoteTo            string
	RepInfo              *reps.Rep // the account's representative, when we score it
	SelfRep              *reps.Rep // this account itself, when it's a scored rep
	Address              string
	Known                known.Account
	IsKnown              bool
	Opened               bool
	Info                 node.AccountInfo
	RepName              string
	Tab                  string
	Txs                  []txRow
	Older                string // head for the next page
	Rcv                  []rcvRow
	RcvMore              bool
	Dgs                  []dgRow
	DgCount              string
	DgMore               bool
	DgOver1              int
	IsRep                bool // has delegated weight
	Unconfirmed          int64
	Head                 string
}

func (s *Server) account(w http.ResponseWriter, r *http.Request) {
	raw := r.PathValue("addr")
	addr := strings.ToLower(strings.TrimSpace(raw))
	if strings.HasPrefix(addr, "xrb_") || strings.HasPrefix(addr, "nano_") {
		s.render(w, r, http.StatusNotFound, "search", page{Title: "Not a Banano address", Data: searchData{Query: raw, Problem: "nano"}})
		return
	}
	if _, err := ban.ParseAddress(addr); err != nil {
		prob := "nomatch"
		if errors.Is(err, ban.ErrChecksum) {
			prob = "checksum"
		}
		s.render(w, r, http.StatusNotFound, "search", page{Title: "Address not valid", Data: searchData{Query: raw, Problem: prob}})
		return
	}
	if addr != raw {
		http.Redirect(w, r, "/account/"+addr+queryString(r), http.StatusMovedPermanently)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	d := &accountData{Address: addr, Tab: r.URL.Query().Get("tab")}
	switch d.Tab {
	case "receivable", "delegators", "insights":
	default:
		d.Tab = "transactions"
	}
	d.Known, d.IsKnown = s.Known.Get(addr)
	info, err := s.Node.AccountInfo(ctx, addr)
	switch {
	case errors.Is(err, node.ErrNotFound):
		// Valid but never opened: it may still have payments waiting.
		d.Info.Balance = "0"
		d.Info.Weight = weightOf(ctx, s, addr)
		if d.Tab == "transactions" {
			d.Tab = "receivable"
		}
	case err != nil:
		s.nodeDown(w, r, err)
		return
	default:
		d.Opened = true
		d.Info = info
		d.RepName = s.Known.Name(info.Representative)
		if snap := s.snapshot(ctx); snap != nil {
			d.RepInfo = snap.By[info.Representative]
		}
		d.Unconfirmed = atoi(info.BlockCount) - atoi(info.ConfirmedHeight)
	}
	if d.Info.Receivable == "" {
		d.Info.Receivable = receivableSum(ctx, s, addr)
	}
	// One cached receivable lookup serves the tab count and the tab itself; for
	// heavily spammed accounts (burn addresses) it's seconds of node work.
	var rcv *rcvList
	if !isZero(d.Info.Receivable) {
		if v, err := s.remember(ctx, "rcv:"+addr, 3*time.Minute, func(ctx context.Context) (any, error) { return s.loadReceivable(ctx, addr) }); err == nil {
			rcv = v.(*rcvList)
			d.RcvCount = ban.Group(fmt.Sprint(rcv.Count))
			if rcv.Count >= 1000 {
				d.RcvCount = "1,000+"
			}
		}
	} else {
		d.RcvCount = "0"
		rcv = &rcvList{}
	}
	if snap := s.snapshot(ctx); snap != nil {
		d.SelfRep = snap.By[addr]
	}
	// Delegators come from the hourly ledger scan, never from the node on demand.
	sc := s.ledgerScan()
	if sc != nil && sc.DelegatorCount[addr] > 0 {
		d.IsRep = true
		d.DgCount = fmt.Sprint(sc.DelegatorCount[addr])
	}

	switch d.Tab {
	case "transactions":
		q := r.URL.Query()
		head := q.Get("head")
		if head != "" && !ban.IsHash(head) {
			head = ""
		}
		switch t := q.Get("type"); t {
		case "send", "receive", "change":
			d.Filter = t
		}
		// Jump to height. The node walks block by block from an end, so start
		// from whichever end is closer and give up after a few seconds.
		if hs := strings.ReplaceAll(q.Get("height"), ",", ""); hs != "" && d.Opened {
			total := atoi(d.Info.BlockCount)
			if hgt := atoi(hs); hgt >= 1 && hgt <= total {
				p := map[string]any{"account": addr, "count": "1", "offset": fmt.Sprint(total - hgt)}
				if hgt-1 < total-hgt {
					p = map[string]any{"account": addr, "count": "1", "offset": fmt.Sprint(hgt - 1), "reverse": "true", "head": d.Info.OpenBlock}
				}
				var at struct {
					History []node.HistoryEntry `json:"history"`
				}
				jctx, jcancel := context.WithTimeout(ctx, 5*time.Second)
				err := s.Node.Call(jctx, "account_history", p, &at)
				jcancel()
				if err == nil && len(at.History) == 1 {
					head = at.History[0].Hash
				} else {
					d.JumpFailed = true
				}
			} else {
				d.JumpFailed = true
			}
		}
		d.Head = head
		// With a type filter, read up to 5,000 blocks to find a page of matches.
		budget := pageSize
		if d.Filter != "" {
			budget = 5000
		}
		scanned := 0
		cur := head
		for scanned < budget && len(d.Txs) < pageSize {
			n := pageSize
			if d.Filter != "" {
				n = 500
			}
			h, err := s.Node.AccountHistory(ctx, addr, n, cur)
			if err != nil && !errors.Is(err, node.ErrNotFound) {
				s.nodeDown(w, r, err)
				return
			}
			if h.Account != "" && h.Account != addr {
				// head belongs to another account: never show its blocks here.
				http.Redirect(w, r, "/account/"+addr, http.StatusFound)
				return
			}
			for _, e := range h.History {
				scanned++
				t := txRow{Height: ban.Group(e.Height), Type: blockType(e.Type, ""), Hash: e.Hash, Amount: e.Amount, Time: atoi(e.LocalTimestamp), Pending: e.Confirmed == "false"}
				if t.Type == "change" {
					t.Party = e.Representative
				} else {
					t.Party = e.Account
				}
				if t.Type == "receive" && e.Previous == strings.Repeat("0", 64) {
					t.Type = "open"
				}
				d.Older = e.Previous
				if d.Filter != "" && !(t.Type == d.Filter || (d.Filter == "receive" && t.Type == "open")) {
					continue
				}
				t.PartyName = s.Known.Name(t.Party)
				d.Txs = append(d.Txs, t)
				if len(d.Txs) == pageSize {
					break
				}
			}
			if len(d.Txs) == pageSize {
				break // stopped mid-page: Older points at the next unread block
			}
			if len(h.History) < n || d.Older == "" || d.Older == strings.Repeat("0", 64) {
				d.Older = ""
				break
			}
			cur = d.Older
		}
		d.Scanned = scanned
		if len(d.Txs) > 0 {
			d.FromHeight, d.ToHeight = d.Txs[0].Height, d.Txs[len(d.Txs)-1].Height
		}
	case "receivable":
		if rcv == nil {
			s.nodeDown(w, r, errors.New("receivable lookup failed"))
			return
		}
		for _, row := range rcv.Rows {
			row.FromName = s.Known.Name(row.From)
			d.Rcv = append(d.Rcv, row)
		}
		d.RcvMore = rcv.Count > len(rcv.Rows)
	case "insights":
		if s.Insights != nil && d.Opened {
			res, _ := s.Insights.Get(addr)
			d.Ins = &res
			if res.Received != nil {
				d.InsYears, d.InsMax = years(res.Years)
				if len(res.Daily) >= 2 {
					var xs []int64
					var ys []float64
					for _, p := range res.Daily {
						xs = append(xs, p.Day)
						ys = append(ys, p.BAN)
					}
					if res.Done { // carry the last balance up to today
						xs = append(xs, time.Now().Unix())
						ys = append(ys, ys[len(ys)-1])
					}
					d.InsChart = lineChart("Balance over time", " BAN", []Line{{Name: "Balance", Color: "var(--c1)", Step: true, X: xs, Y: ys}})
				}
				d.InsNote, d.InsNoteTo = insightNote(s, &res)
			}
		}
	case "delegators":
		if sc == nil {
			d.DgPending = true
			break
		}
		weight := ban.Raw(d.Info.Weight)
		for _, h := range sc.TopDelegators[addr] {
			d.Dgs = append(d.Dgs, dgRow{Account: h.Account, Name: s.Known.Name(h.Account), Balance: h.Balance.String(), Pct: ratio(h.Balance, weight) * 100})
		}
		d.DgOver1 = sc.DelegatorCount[addr]
		d.DgMore = d.DgOver1 > len(d.Dgs)
	}

	title := ban.Short(addr)
	if d.IsKnown {
		title = d.Known.Alias
	}
	desc := "Banano account " + addr + ": balance " + ban.FormatSmart(ban.Raw(d.Info.Balance)) + " BAN."
	s.render(w, r, http.StatusOK, "account", page{Title: title, Description: desc, Data: d, Image: "/og/account/" + addr + ".png"})
}

func weightOf(ctx context.Context, s *Server, addr string) string {
	var r struct {
		Weight string `json:"weight"`
	}
	if s.Node.Call(ctx, "account_weight", map[string]any{"account": addr}, &r) != nil || r.Weight == "" {
		return "0"
	}
	return r.Weight
}

func receivableSum(ctx context.Context, s *Server, addr string) string {
	v, err := s.remember(ctx, "rsum:"+addr, 3*time.Minute, func(ctx context.Context) (any, error) { return receivableSumNow(ctx, s, addr), nil })
	if err != nil {
		return "0"
	}
	return v.(string)
}

func receivableSumNow(ctx context.Context, s *Server, addr string) string {
	var r struct {
		Receivable string `json:"receivable"`
	}
	if s.Node.Call(ctx, "account_balance", map[string]any{"account": addr}, &r) != nil || r.Receivable == "" {
		return "0"
	}
	return r.Receivable
}

func queryString(r *http.Request) string {
	if r.URL.RawQuery == "" {
		return ""
	}
	return "?" + r.URL.RawQuery
}

func isZero(raw string) bool { return ban.Raw(raw).Sign() == 0 }

type yearRow struct {
	Year  int
	Count int64
}

func years(m map[int]int64) ([]yearRow, int64) {
	var out []yearRow
	var mx int64
	for y, n := range m {
		out = append(out, yearRow{y, n})
		mx = max(mx, n)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Year < out[j].Year })
	return out, mx
}

// insightNote spots simple patterns worth a sentence at the top.
func insightNote(s *Server, r *insights.Result) (string, string) {
	if !r.Done || r.Received.Sign() == 0 || len(r.TopRecipients) == 0 {
		return "", ""
	}
	top := r.TopRecipients[0]
	if share := ratio(top.Amount, r.Received); share >= 0.9 && r.RecvN >= 10 {
		name := s.Known.Name(top.Account)
		if name == "" {
			name = ban.Short(top.Account)
		}
		return fmt.Sprintf("%.1f%% of everything it received went straight on to", share*100), name
	}
	return "", ""
}

// exportCSV streams an account's whole history as CSV, newest first. Only one
// export runs at a time across the site so a huge account can't hog the node.
func (s *Server) exportCSV(w http.ResponseWriter, r *http.Request) {
	addr := strings.ToLower(r.PathValue("addr"))
	if _, err := ban.ParseAddress(addr); err != nil {
		http.NotFound(w, r)
		return
	}
	select {
	case s.exportSlot <- struct{}{}:
		defer func() { <-s.exportSlot }()
	default:
		w.Header().Set("Retry-After", "30")
		http.Error(w, "Another export is running. Try again in a minute.", http.StatusServiceUnavailable)
		return
	}
	rc := http.NewResponseController(w)
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+addr+`.csv"`)
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"height", "hash", "type", "counterparty", "amount_ban", "amount_raw", "balance_ban", "time_utc"})
	head := ""
	for {
		_ = rc.SetWriteDeadline(time.Now().Add(60 * time.Second))
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		h, err := s.Node.AccountHistory(ctx, addr, 1000, head)
		cancel()
		if err != nil || (h.Account != "" && h.Account != addr) {
			return
		}
		for _, e := range h.History {
			t := blockType(e.Type, "")
			party := e.Account
			if t == "change" {
				party = e.Representative
			}
			ts := ""
			if n := atoi(e.LocalTimestamp); n > 0 {
				ts = time.Unix(n, 0).UTC().Format(time.RFC3339)
			}
			_ = cw.Write([]string{e.Height, e.Hash, t, party, plain(e.Amount), e.Amount, plain(e.Balance), ts})
		}
		cw.Flush()
		if cw.Error() != nil || len(h.History) < 1000 || h.Previous == "" {
			return
		}
		head = h.Previous
	}
}

// plain is a BAN amount without thousands separators, for CSV.
func plain(raw string) string { return strings.ReplaceAll(ban.Format(ban.Raw(raw), 29), ",", "") }

type rcvList struct {
	Rows  []rcvRow // biggest first, at most pageSize
	Count int      // receivable blocks, capped at 1,000
}

func (s *Server) loadReceivable(ctx context.Context, addr string) (*rcvList, error) {
	m, err := s.Node.Receivable(ctx, addr, 1000, "")
	if err != nil {
		return nil, err
	}
	l := &rcvList{Count: len(m)}
	for h, b := range m {
		l.Rows = append(l.Rows, rcvRow{Hash: h, From: b.Source, Amount: b.Amount})
	}
	sort.Slice(l.Rows, func(i, j int) bool { return ban.Raw(l.Rows[i].Amount).Cmp(ban.Raw(l.Rows[j].Amount)) > 0 })
	if len(l.Rows) > pageSize {
		l.Rows = l.Rows[:pageSize]
	}
	hashes := make([]string, len(l.Rows))
	for i, r := range l.Rows {
		hashes[i] = r.Hash
	}
	if infos, err := s.Node.BlocksInfo(ctx, hashes); err == nil {
		for i := range l.Rows {
			l.Rows[i].Time = atoi(infos[l.Rows[i].Hash].LocalTimestamp)
		}
	}
	return l, nil
}
