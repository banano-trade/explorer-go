// Package monitors reads representatives' Nano Node Monitor pages
// (http://<node ip>/api.php), which is where reps publish their node's name.
// It's how Creeper/spyglass named reps, so reps that set a name there get it
// here too.
package monitors

import (
	"context"
	_ "embed"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/banano-trade/explorer-go/internal/ban"
	"github.com/banano-trade/explorer-go/internal/node"
)

// curated are monitor URLs reps registered with spyglass (MIT, dev-ptera;
// server/database/banano/monitored-representative.json, synced 2026-10-23),
// for monitors that don't live on the node's own IP. They are trusted as is.
//
//go:embed curated.json
var curatedJSON []byte

// Monitor is what a rep's monitor says about its node.
type Monitor struct {
	Account string
	Name    string
	URL     string // the monitor page, for linking
	Version string
	Peers   int
	Seen    time.Time
}

type Reader struct {
	Node *node.Client
	Log  *slog.Logger

	mu    sync.RWMutex
	byAcc map[string]Monitor
}

// Get returns the monitor for a rep account.
func (r *Reader) Get(acct string) (Monitor, bool) {
	if r == nil {
		return Monitor{}, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	m, ok := r.byAcc[acct]
	return m, ok
}

// Name returns the name a rep's monitor publishes, or "".
func (r *Reader) Name(acct string) string {
	m, _ := r.Get(acct)
	return m.Name
}

// Run refreshes every 10 minutes.
func (r *Reader) Run(ctx context.Context) {
	for {
		r.refresh(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(10 * time.Minute):
		}
	}
}

var client = &http.Client{
	Timeout: 6 * time.Second,
	// Never follow redirects: a monitor must answer where it is.
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	Transport: &http.Transport{
		// Only public addresses: peers can't steer us at our own network.
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
			if err != nil || len(ips) == 0 {
				return nil, err
			}
			ip := ips[0].IP
			if !public(ip) {
				return nil, &net.AddrError{Err: "not a public address", Addr: ip.String()}
			}
			return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		},
		TLSHandshakeTimeout: 5 * time.Second,
		MaxIdleConns:        10,
	},
}

func public(ip net.IP) bool {
	return !(ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast())
}

type target struct {
	url     string
	ip      string // the peer IP it came from ("" for curated)
	curated bool
}

func (r *Reader) refresh(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	// Which IP each principal rep votes from: a monitor may only name a
	// principal rep if it runs on that IP (or is curated).
	repIP := map[string]string{}
	var q node.Quorum
	if err := r.Node.Call(ctx, "confirmation_quorum", map[string]any{"peer_details": "true"}, &q); err == nil {
		for _, p := range q.Peers {
			if host, _, err := net.SplitHostPort(p.IP); err == nil {
				repIP[p.Account] = cleanIP(host)
			}
		}
	}
	seen := map[string]bool{}
	var targets []target
	add := func(t target) {
		if !seen[t.url] {
			seen[t.url] = true
			targets = append(targets, t)
		}
	}
	var peers struct {
		Peers map[string]json.RawMessage `json:"peers"`
	}
	if err := r.Node.Call(ctx, "peers", nil, &peers); err == nil {
		for addr := range peers.Peers {
			if host, _, err := net.SplitHostPort(addr); err == nil {
				ip := cleanIP(host)
				if p := net.ParseIP(ip); p != nil && public(p) {
					add(target{url: "http://" + hostPort(ip) + "/api.php", ip: ip})
				}
			}
		}
	}
	var curated []string
	_ = json.Unmarshal(curatedJSON, &curated)
	for _, u := range curated {
		if !strings.HasPrefix(u, "http") {
			u = "http://" + u + "/api.php"
		}
		add(target{url: u, curated: true})
	}

	out := map[string]Monitor{}
	var mu sync.Mutex
	sem := make(chan struct{}, 8)
	var wg sync.WaitGroup
	for _, t := range targets {
		wg.Add(1)
		sem <- struct{}{}
		go func(t target) {
			defer wg.Done()
			defer func() { <-sem }()
			m, ok := fetch(ctx, t.url)
			if !ok {
				return
			}
			if want, principal := repIP[m.Account]; principal && !t.curated && want != t.ip {
				return // claims a principal rep it doesn't run
			}
			mu.Lock()
			if old, dup := out[m.Account]; !dup || (old.URL != "" && t.ip == repIP[m.Account]) {
				out[m.Account] = m
			}
			mu.Unlock()
		}(t)
	}
	wg.Wait()
	if len(out) == 0 && ctx.Err() != nil {
		return
	}
	r.mu.Lock()
	r.byAcc = out
	r.mu.Unlock()
	r.Log.Info("node monitors", "found", len(out), "checked", len(targets))
}

func fetch(ctx context.Context, url string) (Monitor, bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Monitor{}, false
	}
	req.Header.Set("User-Agent", "BananoExplorer/1 (+https://explorer.banano.trade)")
	resp, err := client.Do(req)
	if err != nil {
		return Monitor{}, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Monitor{}, false
	}
	var v struct {
		// Nano Node Monitor
		Account string `json:"nanoNodeAccount"`
		Name    string `json:"nanoNodeName"`
		Version string `json:"version"`
		Peers   any    `json:"numPeers"`
		// Eulen's monitor
		Account2 string `json:"node_account"`
		Name2    string `json:"node_name"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 256<<10)).Decode(&v) != nil {
		return Monitor{}, false
	}
	if v.Account == "" {
		v.Account, v.Name = v.Account2, v.Name2
	}
	acct := strings.ToLower(strings.TrimSpace(v.Account))
	if _, err := ban.ParseAddress(acct); err != nil {
		return Monitor{}, false
	}
	name := cleanName(v.Name)
	if name == "" {
		return Monitor{}, false
	}
	page := strings.TrimSuffix(url, "/api.php")
	return Monitor{Account: acct, Name: name, URL: page, Version: strings.TrimSpace(v.Version), Seen: time.Now()}, true
}

// cleanName keeps names short, printable and free of URL noise.
func cleanName(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(strings.TrimPrefix(s, "https://"), "http://")
	s = strings.TrimSuffix(s, "/")
	var b strings.Builder
	n := 0
	space := false
	for _, r := range s {
		if !unicode.IsSpace(r) && (!unicode.IsPrint(r) || r == '<' || r == '>') {
			continue
		}
		if unicode.IsSpace(r) {
			if space {
				continue
			}
			space, r = true, ' '
		} else {
			space = false
		}
		if n == 40 {
			break
		}
		b.WriteRune(r)
		n++
	}
	return strings.TrimSpace(b.String())
}

func cleanIP(h string) string { return strings.TrimPrefix(strings.Trim(h, "[]"), "::ffff:") }

func hostPort(ip string) string {
	if strings.Contains(ip, ":") {
		return "[" + ip + "]"
	}
	return ip
}

// Search returns monitors whose name contains q (case-insensitive), prefix matches first.
func (r *Reader) Search(q string, limit int) []Monitor {
	if r == nil {
		return nil
	}
	q = strings.ToLower(strings.TrimSpace(q))
	if q == "" {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	var pre, mid []Monitor
	for _, m := range r.byAcc {
		n := strings.ToLower(m.Name)
		switch {
		case strings.HasPrefix(n, q):
			pre = append(pre, m)
		case strings.Contains(n, q):
			mid = append(mid, m)
		}
	}
	out := append(pre, mid...)
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}
