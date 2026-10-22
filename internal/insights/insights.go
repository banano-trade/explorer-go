// Package insights works out an account's lifetime numbers (totals, top
// counterparties, busiest years) by reading its whole history once, in the
// background, then keeping up with new blocks.
package insights

import (
	"context"
	"log/slog"
	"math/big"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/banano-trade/explorer-go/internal/ban"
	"github.com/banano-trade/explorer-go/internal/node"
)

// Party is a counterparty total.
type Party struct {
	Account string
	Amount  *big.Int
	Count   int
}

// Result is what the insights tab shows.
type Result struct {
	Account         string
	Done            bool
	Blocks, Total   int64 // processed so far, and the account's block count when started
	Received, Sent  *big.Int
	RecvN, SendN    int64
	MaxBalance      *big.Int
	MaxBalanceAt    int64
	First, Last     int64 // unix seconds
	Years           map[int]int64
	LargestIn       Party
	LargestOut      Party
	TopSenders      []Party
	TopRecipients   []Party
	Updated         time.Time
	Err             bool // the last run failed; retried on the next view
	lastHash        string
	senders, recips map[string]*Party
}

// Progress is how far along a running job is, 0..100.
func (r *Result) Progress() int {
	if r.Total <= 0 {
		return 0
	}
	return int(min(99, r.Blocks*100/r.Total))
}

// Engine runs jobs one at a time and caches results.
type Engine struct {
	Node *node.Client
	Log  *slog.Logger

	mu      sync.Mutex
	results map[string]*Result
	queue   chan string
	queued  map[string]bool
}

const (
	maxCached  = 100
	maxParties = 2000 // per direction; small counterparties get dropped beyond this
	pageSize   = 1000
)

func NewEngine(n *node.Client, log *slog.Logger) *Engine {
	return &Engine{Node: n, Log: log, results: map[string]*Result{}, queue: make(chan string, 64), queued: map[string]bool{}}
}

// Get returns a snapshot of the account's insights, starting or refreshing the
// job when needed. The result may be partial (Done false).
func (e *Engine) Get(addr string) (Result, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	r, ok := e.results[addr]
	stale := !ok || (r.Done && time.Since(r.Updated) > time.Minute) || (!r.Done && time.Since(r.Updated) > 2*time.Minute)
	if stale && !e.queued[addr] {
		select {
		case e.queue <- addr:
			e.queued[addr] = true
		default: // queue full: try again on the next view
		}
	}
	if !ok {
		return Result{Account: addr}, false
	}
	return r.copy(), true
}

func (r *Result) copy() Result {
	c := *r
	c.Received, c.Sent, c.MaxBalance = new(big.Int).Set(r.Received), new(big.Int).Set(r.Sent), new(big.Int).Set(r.MaxBalance)
	c.Years = make(map[int]int64, len(r.Years))
	for y, n := range r.Years {
		c.Years[y] = n
	}
	c.TopSenders = append([]Party(nil), r.TopSenders...)
	c.TopRecipients = append([]Party(nil), r.TopRecipients...)
	c.senders, c.recips = nil, nil
	return c
}

func (e *Engine) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case addr := <-e.queue:
			if err := e.work(ctx, addr); err != nil && ctx.Err() == nil {
				e.Log.Warn("insights", "account", addr, "err", err)
			}
			e.mu.Lock()
			delete(e.queued, addr)
			e.mu.Unlock()
		}
	}
}

func (e *Engine) work(ctx context.Context, addr string) error {
	e.mu.Lock()
	r := e.results[addr]
	if r == nil {
		if len(e.results) >= maxCached {
			e.evict()
		}
		r = &Result{Account: addr, Received: new(big.Int), Sent: new(big.Int), MaxBalance: new(big.Int), Years: map[int]int64{},
			senders: map[string]*Party{}, recips: map[string]*Party{}, Updated: time.Now()}
		e.results[addr] = r
	}
	r.Done = false
	head := r.lastHash
	e.mu.Unlock()

	info, err := e.Node.AccountInfo(ctx, addr)
	if err != nil {
		e.mu.Lock()
		r.Done, r.Updated = true, time.Now()
		e.mu.Unlock()
		return err
	}
	e.mu.Lock()
	r.Total = atoi(info.BlockCount)
	e.mu.Unlock()
	if head == "" {
		head = info.OpenBlock
	}
	skipFirst := r.lastHash != ""
	fail := func(err error) error {
		e.mu.Lock()
		r.Done, r.Err, r.Updated = true, true, time.Now()
		e.mu.Unlock()
		return err
	}
	for {
		h, err := e.history(ctx, addr, head)
		if err != nil {
			return fail(err)
		}
		e.mu.Lock()
		for i, b := range h {
			if i == 0 && skipFirst {
				continue
			}
			r.add(b)
		}
		if len(h) > 0 {
			r.lastHash = h[len(h)-1].Hash
		}
		r.TopSenders, r.TopRecipients = top(r.senders, 5), top(r.recips, 5)
		r.Updated = time.Now()
		e.mu.Unlock()
		if len(h) < pageSize {
			break
		}
		head, skipFirst = r.lastHash, true
		// Be gentle with the node: it serves the whole site.
		select {
		case <-ctx.Done():
			return fail(ctx.Err())
		case <-time.After(20 * time.Millisecond):
		}
	}
	e.mu.Lock()
	r.Done, r.Err, r.Updated = true, false, time.Now()
	e.mu.Unlock()
	return nil
}

func (e *Engine) history(ctx context.Context, addr, head string) ([]node.HistoryEntry, error) {
	var out struct {
		History []node.HistoryEntry `json:"history"`
	}
	err := e.Node.Call(ctx, "account_history", map[string]any{"account": addr, "count": strconv.Itoa(pageSize), "raw": "true", "reverse": "true", "head": head}, &out)
	for i := range out.History {
		if s := out.History[i].Subtype; s != "" {
			out.History[i].Type = s
		}
	}
	return out.History, err
}

// evict drops the least recently updated result. Caller holds mu.
func (e *Engine) evict() {
	var oldest string
	var at time.Time
	for a, r := range e.results {
		if r.Done && (oldest == "" || r.Updated.Before(at)) {
			oldest, at = a, r.Updated
		}
	}
	if oldest != "" {
		delete(e.results, oldest)
	}
}

func (r *Result) add(b node.HistoryEntry) {
	r.Blocks++
	t := atoi(b.LocalTimestamp)
	if t > 0 {
		if r.First == 0 || t < r.First {
			r.First = t
		}
		r.Last = max(r.Last, t)
		r.Years[time.Unix(t, 0).UTC().Year()]++
	}
	amt := ban.Raw(b.Amount)
	switch b.Type {
	case "receive", "open":
		r.Received.Add(r.Received, amt)
		r.RecvN++
		bump(r.senders, b.Account, amt)
		if r.LargestIn.Amount == nil || amt.Cmp(r.LargestIn.Amount) > 0 {
			r.LargestIn = Party{Account: b.Account, Amount: amt}
		}
	case "send":
		r.Sent.Add(r.Sent, amt)
		r.SendN++
		bump(r.recips, b.Account, amt)
		if r.LargestOut.Amount == nil || amt.Cmp(r.LargestOut.Amount) > 0 {
			r.LargestOut = Party{Account: b.Account, Amount: amt}
		}
	}
	if bal := ban.Raw(b.Balance); b.Balance != "" && bal.Cmp(r.MaxBalance) > 0 {
		r.MaxBalance, r.MaxBalanceAt = bal, t
	}
}

func bump(m map[string]*Party, a string, amt *big.Int) {
	if a == "" {
		return
	}
	p := m[a]
	if p == nil {
		if len(m) >= maxParties {
			prune(m)
		}
		p = &Party{Account: a, Amount: new(big.Int)}
		m[a] = p
	}
	p.Amount.Add(p.Amount, amt)
	p.Count++
}

// prune keeps the bigger half of counterparties, so busy accounts stay bounded.
func prune(m map[string]*Party) {
	l := make([]*Party, 0, len(m))
	for _, p := range m {
		l = append(l, p)
	}
	sort.Slice(l, func(i, j int) bool { return l[i].Amount.Cmp(l[j].Amount) > 0 })
	for _, p := range l[len(l)/2:] {
		delete(m, p.Account)
	}
}

func top(m map[string]*Party, n int) []Party {
	l := make([]Party, 0, len(m))
	for _, p := range m {
		l = append(l, Party{Account: p.Account, Amount: new(big.Int).Set(p.Amount), Count: p.Count})
	}
	sort.Slice(l, func(i, j int) bool { return l[i].Amount.Cmp(l[j].Amount) > 0 })
	if len(l) > n {
		l = l[:n]
	}
	return l
}

func atoi(s string) int64 {
	n, _ := strconv.ParseInt(s, 10, 64)
	return n
}
