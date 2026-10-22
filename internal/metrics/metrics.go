// Package metrics samples network numbers once a minute for the history
// charts. It keeps a day of minutes and 60 days of hourly averages on disk.
package metrics

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/banano-trade/explorer-go/internal/ban"
	"github.com/banano-trade/explorer-go/internal/node"
)

// Point is one sample (or the average of an hour of them).
type Point struct {
	T         int64   `json:"t"`      // unix seconds
	CPS       float64 `json:"cps"`    // confirmations per second
	OnlineBAN float64 `json:"online"` // online weight in BAN
	Peers     float64 `json:"peers"`
	Backlog   float64 `json:"backlog"` // unconfirmed blocks
	Latest    float64 `json:"latest"`  // share of official peers on the newest version, 0..1
}

// Series names, used by the network page tabs.
var Series = []struct{ Key, Name, Unit string }{
	{"cps", "Confirmations / s", ""},
	{"online", "Online weight", " BAN"},
	{"peers", "Peers", ""},
	{"latest", "On newest version", "%"},
	{"backlog", "Backlog", " blocks"},
}

func (p Point) Get(key string) float64 {
	switch key {
	case "cps":
		return p.CPS
	case "online":
		return p.OnlineBAN
	case "peers":
		return p.Peers
	case "latest":
		return p.Latest * 100
	case "backlog":
		return p.Backlog
	}
	return 0
}

type Sampler struct {
	Node *node.Client `json:"-"`
	Path string       `json:"-"`
	Log  *slog.Logger `json:"-"`

	mu       sync.RWMutex
	Minutes  []Point `json:"minutes"`
	Hours    []Point `json:"hours"`
	lastCem  int64
	lastTime time.Time
}

const (
	keepMinutes = 1440
	keepHours   = 60 * 24
)

func NewSampler(n *node.Client, path string, log *slog.Logger) *Sampler {
	s := &Sampler{Node: n, Path: path, Log: log}
	if path != "" {
		b, err := os.ReadFile(path)
		if err == nil {
			if err := json.Unmarshal(b, s); err != nil {
				log.Warn("metrics file", "err", err)
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			log.Warn("metrics file", "err", err)
		}
	}
	return s
}

func (s *Sampler) Run(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	n := 0
	for {
		s.sample(ctx)
		if n++; n%10 == 0 {
			s.save()
		}
		select {
		case <-ctx.Done():
			s.save()
			return
		case <-t.C:
		}
	}
}

func (s *Sampler) sample(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	now := time.Now()
	bc, err := s.Node.BlockCount(ctx)
	if err != nil {
		return
	}
	p := Point{T: now.Unix()}
	cem := atoi(bc.Cemented)
	p.Backlog = float64(atoi(bc.Count) - cem)
	if !s.lastTime.IsZero() && cem >= s.lastCem {
		p.CPS = float64(cem-s.lastCem) / now.Sub(s.lastTime).Seconds()
	}
	s.lastCem, s.lastTime = cem, now
	if q, err := s.Node.ConfirmationQuorum(ctx, false); err == nil {
		p.OnlineBAN = ban.Float(ban.Raw(q.OnlineStakeTotal))
	}
	if n, err := s.Node.PeerCount(ctx); err == nil {
		p.Peers = float64(n)
	}
	if tel, err := s.Node.TelemetryRaw(ctx); err == nil {
		vs := map[string]int{}
		total := 0
		best := ""
		for _, t := range tel {
			if t.Maker != "0" {
				continue
			}
			v := t.MajorVersion + "." + t.MinorVersion
			vs[v]++
			total++
			if best == "" || newer(v, best) {
				best = v
			}
		}
		if total > 0 {
			p.Latest = float64(vs[best]) / float64(total)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Minutes = append(s.Minutes, p)
	if len(s.Minutes) > keepMinutes {
		s.Minutes = s.Minutes[len(s.Minutes)-keepMinutes:]
	}
	s.rollHours(now)
}

// rollHours averages every complete hour of minutes into Hours. Caller holds mu.
func (s *Sampler) rollHours(now time.Time) {
	lastHour := int64(0)
	if len(s.Hours) > 0 {
		lastHour = s.Hours[len(s.Hours)-1].T
	}
	cur := now.Truncate(time.Hour).Unix()
	sums := map[int64]*Point{}
	counts := map[int64]int{}
	var order []int64
	for _, m := range s.Minutes {
		h := time.Unix(m.T, 0).Truncate(time.Hour).Unix()
		if h <= lastHour || h >= cur {
			continue
		}
		if sums[h] == nil {
			sums[h] = &Point{T: h}
			order = append(order, h)
		}
		a := sums[h]
		a.CPS += m.CPS
		a.OnlineBAN += m.OnlineBAN
		a.Peers += m.Peers
		a.Backlog += m.Backlog
		a.Latest += m.Latest
		counts[h]++
	}
	for _, h := range order {
		a, n := sums[h], float64(counts[h])
		a.CPS /= n
		a.OnlineBAN /= n
		a.Peers /= n
		a.Backlog /= n
		a.Latest /= n
		s.Hours = append(s.Hours, *a)
	}
	if len(s.Hours) > keepHours {
		s.Hours = s.Hours[len(s.Hours)-keepHours:]
	}
}

// Range returns points covering the last d: minutes for a day or less, hours beyond.
func (s *Sampler) Range(d time.Duration) []Point {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cut := time.Now().Add(-d).Unix()
	src := s.Hours
	if d <= 24*time.Hour {
		src = s.Minutes
	}
	var out []Point
	for _, p := range src {
		if p.T >= cut {
			out = append(out, p)
		}
	}
	return out
}

func (s *Sampler) save() {
	if s.Path == "" {
		return
	}
	s.mu.RLock()
	b, err := json.Marshal(s)
	s.mu.RUnlock()
	if err == nil {
		err = os.MkdirAll(filepath.Dir(s.Path), 0o750)
	}
	if err == nil {
		tmp := s.Path + ".tmp"
		if err = os.WriteFile(tmp, b, 0o640); err == nil {
			err = os.Rename(tmp, s.Path)
		}
	}
	if err != nil {
		s.Log.Warn("metrics save", "err", err)
	}
}

func newer(a, b string) bool {
	var am, an, bm, bn int
	parse := func(v string, m, n *int) {
		for i, part := 0, 0; i < len(v); i++ {
			if v[i] == '.' {
				part++
				continue
			}
			if part == 0 {
				*m = *m*10 + int(v[i]-'0')
			} else {
				*n = *n*10 + int(v[i]-'0')
			}
		}
	}
	parse(a, &am, &an)
	parse(b, &bm, &bn)
	return am > bm || (am == bm && an > bn)
}

func atoi(s string) int64 {
	var n int64
	for _, c := range s {
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + int64(c-'0')
	}
	return n
}
