// Package ledger scans every account with a balance once an hour, for the rich
// list, balance buckets, known-account balances, movers and burn totals.
package ledger

import (
	"bytes"
	"context"
	"encoding/gob"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/banano-trade/explorer-go/internal/ban"
	"github.com/banano-trade/explorer-go/internal/node"
)

// Holder is one row of the rich list.
type Holder struct {
	Account  string
	Rep      string
	Balance  *big.Int
	Blocks   int64
	Modified int64 // unix seconds of the last block
}

// Bucket counts accounts in a balance range.
type Bucket struct {
	Label string
	Count int
}

// Mover is an account whose balance changed a lot.
type Mover struct {
	Account string
	Change  float64 // BAN, negative when it shrank
	Now     float64
}

// Burner is one address sending to the burn addresses.
type Burner struct {
	Account string
	Amount  *big.Int
	Burns   int
}

// Scan is one full pass over the ledger.
type Scan struct {
	At        time.Time
	Took      time.Duration
	Accounts  int // with at least 0.0001 BAN
	Top       []Holder
	Buckets   []Bucket
	Balance   map[string]*big.Int // accounts with ≥ 1 BAN; for known-account balances
	Movers24h []Mover
	Movers7d  []Mover
	Burners   []Burner // biggest first
	Burned    *big.Int
	BurnCount int
	// Delegators per representative (accounts with at least 0.0001 BAN), and
	// the 100 biggest of them, so pages never need the node's slow scans.
	DelegatorCount map[string]int
	TopDelegators  map[string][]Holder
}

// Scanner runs scans and keeps the latest.
type Scanner struct {
	Node      *node.Client
	URL       string // node RPC URL, for the streamed ledger call
	Dir       string // where daily balance snapshots go (for movers); empty = no movers
	BurnAddrs []string
	Log       *slog.Logger
	Every     time.Duration

	mu   sync.RWMutex
	last *Scan
}

func (s *Scanner) Last() *Scan {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.last
}

func (s *Scanner) Run(ctx context.Context) {
	if s.Every == 0 {
		s.Every = time.Hour
	}
	for {
		start := time.Now()
		sc, err := s.Scan(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			s.Log.Warn("ledger scan", "err", err)
		} else {
			s.mu.Lock()
			s.last = sc
			s.mu.Unlock()
			s.Log.Info("ledger scan", "accounts", sc.Accounts, "took", sc.Took.Round(time.Second))
		}
		wait := s.Every - time.Since(start)
		if err != nil {
			wait = 5 * time.Minute
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

const (
	pageSize = 20000
	minRaw   = "10000000000000000000000000" // 0.0001 BAN
	keepTop  = 1000
)

var (
	raw1   = new(big.Int).Exp(big.NewInt(10), big.NewInt(29), nil) // 1 BAN
	limits = []float64{1, 10, 100, 1e3, 1e4, 1e5, 1e6, 1e7}
	labels = []string{"under 1", "1–10", "10–100", "100–1K", "1K–10K", "10K–100K", "100K–1M", "1M–10M", "10M+"}
)

// Scan pages through the ledger. It holds only the top holders and the ≥1 BAN
// balances in memory, never the whole response.
func (s *Scanner) Scan(ctx context.Context) (*Scan, error) {
	start := time.Now()
	sc := &Scan{At: start, Balance: map[string]*big.Int{}, DelegatorCount: map[string]int{}, TopDelegators: map[string][]Holder{}}
	counts := make([]int, len(labels))
	next := ban.Address(make([]byte, 32)) // the lowest possible account
	for {
		page, last, err := s.page(ctx, next)
		if err != nil {
			return nil, err
		}
		for _, a := range page {
			if a.Account == next && sc.Accounts > 0 {
				continue // pages overlap on the start account
			}
			sc.Accounts++
			f := ban.Float(a.Balance)
			i := sort.SearchFloat64s(limits, f)
			if i < len(limits) && limits[i] == f {
				i++
			}
			counts[i]++
			if a.Balance.Cmp(raw1) >= 0 {
				sc.Balance[a.Account] = a.Balance
			}
			sc.Top = pushTop(sc.Top, a)
			if a.Rep != "" {
				sc.DelegatorCount[a.Rep]++
				sc.TopDelegators[a.Rep] = pushN(sc.TopDelegators[a.Rep], a, 100)
			}
		}
		if len(page) < pageSize || last == "" || last == next {
			break
		}
		next = last
	}
	for i, l := range labels {
		sc.Buckets = append(sc.Buckets, Bucket{Label: l, Count: counts[i]})
	}
	sort.Slice(sc.Top, func(i, j int) bool { return sc.Top[i].Balance.Cmp(sc.Top[j].Balance) > 0 })
	for _, l := range sc.TopDelegators {
		sort.Slice(l, func(i, j int) bool { return l[i].Balance.Cmp(l[j].Balance) > 0 })
	}
	if err := s.burns(ctx, sc); err != nil {
		s.Log.Warn("burn scan", "err", err)
	}
	if s.Dir != "" {
		if err := s.movers(sc); err != nil {
			s.Log.Warn("movers", "err", err)
		}
	}
	sc.Took = time.Since(start)
	return sc, nil
}

// pushTop keeps the keepTop biggest holders (unsorted until the end).
func pushTop(top []Holder, h Holder) []Holder { return pushN(top, h, keepTop) }

// pushN keeps the n biggest holders (unsorted).
func pushN(top []Holder, h Holder, keepTop int) []Holder {
	if len(top) < keepTop {
		return append(top, h)
	}
	min := 0
	for i := range top {
		if top[i].Balance.Cmp(top[min].Balance) < 0 {
			min = i
		}
	}
	if h.Balance.Cmp(top[min].Balance) > 0 {
		top[min] = h
	}
	return top
}

// page fetches up to pageSize accounts from start on, streaming the JSON so the
// order (by public key) is kept and the last key is known.
func (s *Scanner) page(ctx context.Context, start string) ([]Holder, string, error) {
	body, _ := json.Marshal(map[string]string{"action": "ledger", "account": start, "count": fmt.Sprint(pageSize), "threshold": minRaw, "representative": "true"})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.URL, bytes.NewReader(body))
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	c := &http.Client{Timeout: 5 * time.Minute}
	resp, err := c.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	dec := json.NewDecoder(io.LimitReader(resp.Body, 256<<20))
	// {"accounts": {"ban_...": {...}, ...}}
	if err := expectDelim(dec, '{'); err != nil {
		return nil, "", err
	}
	var out []Holder
	last := ""
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return nil, "", err
		}
		key, _ := t.(string)
		if key == "error" {
			var msg string
			_ = dec.Decode(&msg)
			return nil, "", fmt.Errorf("node ledger: %s", msg)
		}
		if key != "accounts" {
			var skip json.RawMessage
			if err := dec.Decode(&skip); err != nil {
				return nil, "", err
			}
			continue
		}
		if t, err := dec.Token(); err != nil {
			return nil, "", err
		} else if d, ok := t.(json.Delim); !ok || d != '{' {
			return out, last, nil // "" when empty
		}
		for dec.More() {
			t, err := dec.Token()
			if err != nil {
				return nil, "", err
			}
			acct, _ := t.(string)
			var a struct {
				Balance    string `json:"balance"`
				BlockCount string `json:"block_count"`
				Modified   string `json:"modified_timestamp"`
				Rep        string `json:"representative"`
			}
			if err := dec.Decode(&a); err != nil {
				return nil, "", err
			}
			out = append(out, Holder{Account: acct, Rep: a.Rep, Balance: ban.Raw(a.Balance), Blocks: atoi(a.BlockCount), Modified: atoi(a.Modified)})
			last = acct
		}
		if _, err := dec.Token(); err != nil { // closing }
			return nil, "", err
		}
	}
	return out, last, nil
}

func expectDelim(dec *json.Decoder, want json.Delim) error {
	t, err := dec.Token()
	if err != nil {
		return err
	}
	if d, ok := t.(json.Delim); !ok || d != want {
		return fmt.Errorf("unexpected %v", t)
	}
	return nil
}

func (s *Scanner) burns(ctx context.Context, sc *Scan) error {
	sc.Burned = new(big.Int)
	by := map[string]*Burner{}
	for _, a := range s.BurnAddrs {
		m, err := s.Node.Receivable(ctx, a, 2000000, "")
		if err != nil {
			return err
		}
		for _, b := range m {
			amt := ban.Raw(b.Amount)
			sc.Burned.Add(sc.Burned, amt)
			sc.BurnCount++
			br := by[b.Source]
			if br == nil {
				br = &Burner{Account: b.Source, Amount: new(big.Int)}
				by[b.Source] = br
			}
			br.Amount.Add(br.Amount, amt)
			br.Burns++
		}
	}
	for _, b := range by {
		sc.Burners = append(sc.Burners, *b)
	}
	sort.Slice(sc.Burners, func(i, j int) bool { return sc.Burners[i].Amount.Cmp(sc.Burners[j].Amount) > 0 })
	return nil
}

// Daily snapshots of ≥1 BAN balances let us compare with a day and a week ago.
type daily map[string]float64

func (s *Scanner) movers(sc *Scan) error {
	if err := os.MkdirAll(s.Dir, 0o750); err != nil {
		return err
	}
	today := filepath.Join(s.Dir, "balances-"+sc.At.UTC().Format("2006-01-02T15")+".gob")
	cur := daily{}
	for a, b := range sc.Balance {
		cur[a] = ban.Float(b)
	}
	files, _ := filepath.Glob(filepath.Join(s.Dir, "balances-*.gob"))
	sort.Strings(files)
	// One snapshot per hour for 8 days is ~190 files of a few MB: keep one every
	// 6 hours instead, plus the newest.
	if len(files) == 0 || filepath.Base(files[len(files)-1]) < filepath.Base(today) && sc.At.UTC().Hour()%6 == 0 {
		if err := writeGob(today, cur); err != nil {
			return err
		}
		files = append(files, today)
	}
	for _, f := range files {
		if t, ok := snapTime(f); ok && sc.At.Sub(t) > 8*24*time.Hour {
			os.Remove(f)
		}
	}
	pick := func(ago time.Duration) daily {
		want := sc.At.Add(-ago)
		best, bestDiff := "", time.Duration(1<<62)
		for _, f := range files {
			t, ok := snapTime(f)
			if !ok {
				continue
			}
			d := t.Sub(want)
			if d < 0 {
				d = -d
			}
			if d < bestDiff && d < 6*time.Hour {
				best, bestDiff = f, d
			}
		}
		if best == "" {
			return nil
		}
		var m daily
		if readGob(best, &m) != nil {
			return nil
		}
		return m
	}
	sc.Movers24h = diff(pick(24*time.Hour), cur)
	sc.Movers7d = diff(pick(7*24*time.Hour), cur)
	return nil
}

func diff(old, cur daily) []Mover {
	if old == nil {
		return nil
	}
	var out []Mover
	for a, now := range cur {
		if c := now - old[a]; c >= 1000 || c <= -1000 {
			out = append(out, Mover{Account: a, Change: c, Now: now})
		}
	}
	for a, was := range old {
		if _, ok := cur[a]; !ok && was >= 1000 {
			out = append(out, Mover{Account: a, Change: -was})
		}
	}
	sort.Slice(out, func(i, j int) bool { return abs(out[i].Change) > abs(out[j].Change) })
	if len(out) > 20 {
		out = out[:20]
	}
	return out
}

func snapTime(f string) (time.Time, bool) {
	b := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(f), "balances-"), ".gob")
	t, err := time.Parse("2006-01-02T15", b)
	return t, err == nil
}

func writeGob(path string, v any) error {
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if err := gob.NewEncoder(f).Encode(v); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func readGob(path string, v any) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return gob.NewDecoder(f).Decode(v)
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}

func atoi(s string) int64 {
	var n int64
	for _, c := range s {
		if c < '0' || c > '9' {
			return n
		}
		n = n*10 + int64(c-'0')
	}
	return n
}
