// Package bns resolves Banano Name System names like "yusuf.ban" to accounts.
// Everything is on-chain, read with plain account_history calls; this is a Go
// port of the resolver in the `bns` JavaScript library (by Prussia/Banani).
//
// How it works: a TLD account issues a name by sending 0.0012070301–0.00120703011
// BAN to a fresh "domain account" with the name packed into the block's
// representative field. The domain account then:
//   - transfers the name on with another send of that amount (name in rep),
//   - points the name at an address with a send of exactly 4224 raw,
//   - sets metadata with a representative change, or
//   - freezes it by changing representative to 4511…1.
package bns

import (
	"context"
	"encoding/hex"
	"errors"
	"log/slog"
	"math/big"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/banano-trade/explorer-go/internal/ban"
	"github.com/banano-trade/explorer-go/internal/node"
)

// TLDs maps each top-level domain to the account that issues its names.
var TLDs = map[string]string{
	"ban":     "ban_1fdo6b4bqm6pp1w55duuqw5ebz455975o4qcp8of85fjcdw9qhuzxsd3tjb9",
	"jtv":     "ban_3gipeswotbnyemcc1dejyhy5a1zfgj35kw356dommbx4rdochiteajcsay56",
	"mictest": "ban_1dzpfrgi8t4byzmdeidh57p14h5jwbursf1t3ztbmeqnqqdcbpgp9x8j3cw6",
}

var (
	transferMin, _ = new(big.Int).SetString("120703010000000000000000000", 10) // 0.0012070301 BAN
	transferMax, _ = new(big.Int).SetString("120703011000000000000000000", 10) // 0.00120703011 BAN
	resolverRaw    = big.NewInt(4224)
	freezeRep      = mustAddress("451" + strings.Repeat("1", 61))
)

const page = 500

// Domain is a resolved name.
type Domain struct {
	Name, TLD string
	Owner     string // the domain account currently holding the name
	Resolves  string // where the name points ("" if it doesn't point anywhere)
	Frozen    bool
	Burned    bool
	Checked   time.Time
}

// Full is "name.tld".
func (d Domain) Full() string { return d.Name + "." + d.TLD }

// Split parses "name.tld" for a known TLD.
func Split(q string) (name, tld string, ok bool) {
	q = strings.ToLower(strings.TrimSpace(q))
	i := strings.LastIndexByte(q, '.')
	if i <= 0 || i == len(q)-1 {
		return "", "", false
	}
	name, tld = q[:i], q[i+1:]
	if _, known := TLDs[tld]; !known || strings.ContainsAny(name, ".\"\x00 ") || len(name) > 32 {
		return "", "", false
	}
	return name, tld, true
}

// ErrNoName means the TLD never issued that name.
var ErrNoName = errors.New("bns: name not issued")

type entry struct {
	Hash, Subtype, Representative, Link, Amount, Height string
}

func histEntries(h node.History) []entry {
	out := make([]entry, len(h.History))
	for i, e := range h.History {
		out[i] = entry{e.Hash, e.Type, e.Representative, e.Link, e.Amount, e.Height}
	}
	return out
}

// Resolver walks the chain. Index keeps every issued name resolved.
type Resolver struct {
	Node *node.Client
	Log  *slog.Logger

	mu      sync.RWMutex
	names   map[string]Domain   // "yusuf.ban" -> domain
	byAddr  map[string][]string // resolved address -> names
	indexed time.Time
}

// openAndFrontier returns the open block and confirmed frontier, or ok=false if unopened.
func (r *Resolver) openAndFrontier(ctx context.Context, acct string) (open, frontier string, ok bool, err error) {
	info, err := r.Node.AccountInfo(ctx, acct)
	if errors.Is(err, node.ErrNotFound) {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, err
	}
	f := info.ConfirmedFrontier
	if f == "" {
		f = info.Frontier
	}
	return info.OpenBlock, f, true, nil
}

// fromOpen reads history oldest first, starting at head (inclusive).
func (r *Resolver) fromOpen(ctx context.Context, acct, head string) ([]entry, error) {
	var h node.History
	err := r.Node.Call(ctx, "account_history", map[string]any{"account": acct, "count": "500", "raw": "true", "reverse": "true", "head": head}, &h)
	for i := range h.History {
		if s := h.History[i].Subtype; s != "" {
			h.History[i].Type = s
		}
	}
	if err == nil && h.Account != "" && h.Account != acct {
		return nil, errors.New("bns: history head belongs to another account")
	}
	return histEntries(h), err
}

func isTransfer(e entry) bool {
	a, ok := new(big.Int).SetString(e.Amount, 10)
	return ok && e.Subtype == "send" && a.Cmp(transferMin) >= 0 && a.Cmp(transferMax) <= 0
}

// walk calls f for each block from open to the confirmed frontier; f returns stop.
func (r *Resolver) walk(ctx context.Context, acct string, f func(entry) bool) (opened bool, err error) {
	open, frontier, ok, err := r.openAndFrontier(ctx, acct)
	if err != nil || !ok {
		return false, err
	}
	head := open
	for pages := 0; pages < 2000; pages++ {
		es, err := r.fromOpen(ctx, acct, head)
		if err != nil {
			return true, err
		}
		for i, e := range es {
			if pages > 0 && i == 0 {
				continue // the head block was the last of the previous page
			}
			if f(e) || e.Hash == frontier {
				return true, nil
			}
		}
		if len(es) < page {
			return true, nil
		}
		head = es[len(es)-1].Hash
	}
	return true, errors.New("bns: history too long")
}

type issue struct{ to, hash string }

// issued lists every name a TLD account issued, with the account and block it went out in.
func (r *Resolver) issued(ctx context.Context, tld string) (map[string]issue, error) {
	out := map[string]issue{}
	_, err := r.walk(ctx, TLDs[tld], func(e entry) bool {
		if isTransfer(e) {
			if name := decodeName(e.Representative); name != "" {
				if _, dup := out[name]; !dup { // re-issuing is invalid; the first one counts
					out[name] = issue{linkAddress(e.Link), e.Hash}
				}
			}
		}
		return e.Subtype == "change" && e.Representative == freezeRep
	})
	return out, err
}

// follow crawls a domain from the account it was first sent to.
func (r *Resolver) follow(ctx context.Context, d Domain, to, sendHash string) (Domain, error) {
	for hops := 0; hops < 50; hops++ {
		d.Owner = to
		var next, nextHash string
		opened, err := r.walk(ctx, to, func(e entry) bool {
			switch {
			case e.Height == "1":
				if e.Link != sendHash { // opened by something else: the name is burned
					d.Burned = true
					return true
				}
			case isTransfer(e) && decodeName(e.Representative) == d.Name:
				next, nextHash = linkAddress(e.Link), e.Hash
				d.Resolves = ""
				return true
			case e.Subtype == "change" && e.Representative == freezeRep:
				d.Frozen = true
				return true
			case e.Subtype == "send" && e.Amount == resolverRaw.String():
				d.Resolves = linkAddress(e.Link)
			}
			return false
		})
		if err != nil {
			return d, err
		}
		if !opened || next == "" || d.Burned || d.Frozen {
			return d, nil
		}
		to, sendHash = next, nextHash
	}
	return d, nil
}

// Resolve looks a name up on chain (slow path; Lookup checks the index first).
func (r *Resolver) Resolve(ctx context.Context, name, tld string) (Domain, error) {
	d := Domain{Name: name, TLD: tld, Checked: time.Now()}
	var to, hash string
	_, err := r.walk(ctx, TLDs[tld], func(e entry) bool {
		if isTransfer(e) && decodeName(e.Representative) == name {
			to, hash = linkAddress(e.Link), e.Hash
			return true
		}
		return e.Subtype == "change" && e.Representative == freezeRep
	})
	if err != nil {
		return d, err
	}
	if to == "" {
		return d, ErrNoName
	}
	return r.follow(ctx, d, to, hash)
}

// Lookup returns a name from the index, resolving it live if it isn't there.
func (r *Resolver) Lookup(ctx context.Context, q string) (Domain, error) {
	name, tld, ok := Split(q)
	if !ok {
		return Domain{}, ErrNoName
	}
	r.mu.RLock()
	d, hit := r.names[name+"."+tld]
	r.mu.RUnlock()
	if hit && time.Since(d.Checked) < 10*time.Minute {
		return d, nil
	}
	d, err := r.Resolve(ctx, name, tld)
	if err == nil {
		r.mu.Lock()
		if r.names == nil {
			r.names, r.byAddr = map[string]Domain{}, map[string][]string{}
		}
		r.names[d.Full()] = d
		if d.Resolves != "" && !contains(r.byAddr[d.Resolves], d.Full()) {
			r.byAddr[d.Resolves] = append(r.byAddr[d.Resolves], d.Full())
			sortNames(r.byAddr[d.Resolves])
		}
		r.mu.Unlock()
	}
	return d, err
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

// NamesFor returns the names that resolve to addr.
func (r *Resolver) NamesFor(addr string) []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.byAddr[addr]
}

// Search returns indexed names starting with q (for search suggestions).
func (r *Resolver) Search(q string, limit int) []Domain {
	q = strings.ToLower(strings.TrimSpace(q))
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []Domain
	for full, d := range r.names {
		if strings.HasPrefix(full, q) && d.Resolves != "" {
			out = append(out, d)
			if len(out) == limit {
				break
			}
		}
	}
	return out
}

// Run rebuilds the index every half hour.
func (r *Resolver) Run(ctx context.Context) {
	for {
		start := time.Now()
		names, byAddr := map[string]Domain{}, map[string][]string{}
		for tld := range TLDs {
			iss, err := r.issued(ctx, tld)
			if err != nil {
				r.Log.Warn("bns index", "tld", tld, "err", err)
				continue
			}
			for name, is := range iss {
				d, err := r.follow(ctx, Domain{Name: name, TLD: tld, Checked: time.Now()}, is.to, is.hash)
				if err != nil {
					continue
				}
				names[d.Full()] = d
				if d.Resolves != "" {
					byAddr[d.Resolves] = append(byAddr[d.Resolves], d.Full())
				}
			}
		}
		if ctx.Err() != nil {
			return
		}
		for _, l := range byAddr {
			sortNames(l)
		}
		r.mu.Lock()
		r.names, r.byAddr, r.indexed = names, byAddr, time.Now()
		r.mu.Unlock()
		r.Log.Info("bns index", "names", len(names), "took", time.Since(start).Round(time.Second))
		select {
		case <-ctx.Done():
			return
		case <-time.After(30 * time.Minute):
		}
	}
}

// decodeName reads the name packed into a representative address.
func decodeName(rep string) string {
	key, err := ban.ParseAddress(rep)
	if err != nil {
		return ""
	}
	return strings.ReplaceAll(string(key), "\x00", "")
}

// EncodeName packs a name into a public key (for tests and tools).
func EncodeName(name string) []byte {
	b := []byte(name)
	k := make([]byte, 32)
	copy(k[32-len(b):], b)
	return k
}

func linkAddress(link string) string {
	k, err := hex.DecodeString(link)
	if err != nil || len(k) != 32 {
		return ""
	}
	return ban.Address(k)
}

func mustAddress(h string) string {
	k, err := hex.DecodeString(h)
	if err != nil {
		panic(err)
	}
	return ban.Address(k)
}

func (r *Resolver) indexedAt() time.Time {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.indexed
}

// sortNames puts the shortest name first (usually the one people use).
func sortNames(l []string) {
	sort.Slice(l, func(i, j int) bool {
		if len(l[i]) != len(l[j]) {
			return len(l[i]) < len(l[j])
		}
		return l[i] < l[j]
	})
}
