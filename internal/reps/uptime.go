// Package reps tracks representatives: uptime (one check per minute), and the
// rep score shown on the representatives pages.
package reps

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Run is a stretch of minutes a rep was online or offline.
type Run struct {
	Online  bool `json:"on"`
	Minutes int  `json:"n"`
}

type history struct {
	Since time.Time `json:"since"`
	Runs  []Run     `json:"runs"` // oldest first
	Miss  int       `json:"miss"` // consecutive minutes missing from the online list
}

// Uptime records, once a minute, which reps are online.
type Uptime struct {
	Path string

	mu   sync.RWMutex
	reps map[string]*history
	last time.Time
}

const (
	graceMinutes = 5          // a rep only counts as offline after missing 5 checks in a row
	keepMinutes  = 400 * 1440 // a bit over a year
)

func NewUptime(path string) (*Uptime, error) {
	u := &Uptime{Path: path, reps: map[string]*history{}}
	if path == "" {
		return u, nil
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return u, nil
	}
	if err != nil {
		return nil, err
	}
	return u, json.Unmarshal(b, &u.reps)
}

// Record adds one minute. online holds the reps the node sees right now;
// tracked holds every rep worth tracking (online or not), so reps that vanish
// keep accruing offline minutes.
func (u *Uptime) Record(now time.Time, online map[string]bool, tracked map[string]bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.last = now
	for a := range online {
		if _, ok := u.reps[a]; !ok && tracked[a] {
			u.reps[a] = &history{Since: now}
		}
	}
	for a, h := range u.reps {
		on := online[a]
		if on {
			h.Miss = 0
		} else {
			h.Miss++
			if h.Miss < graceMinutes {
				on = true
			} else if h.Miss == graceMinutes {
				// The grace minutes turned out to be an outage after all.
				h.take(graceMinutes - 1)
				h.add(false, graceMinutes-1)
			}
		}
		h.add(on, 1)
		h.trim()
	}
}

func (h *history) add(on bool, n int) {
	if n <= 0 {
		return
	}
	if l := len(h.Runs); l > 0 && h.Runs[l-1].Online == on {
		h.Runs[l-1].Minutes += n
		return
	}
	h.Runs = append(h.Runs, Run{Online: on, Minutes: n})
}

// take removes the newest n minutes.
func (h *history) take(n int) {
	for n > 0 && len(h.Runs) > 0 {
		l := &h.Runs[len(h.Runs)-1]
		if l.Minutes > n {
			l.Minutes -= n
			return
		}
		n -= l.Minutes
		h.Runs = h.Runs[:len(h.Runs)-1]
	}
}

func (h *history) trim() {
	total := 0
	for _, r := range h.Runs {
		total += r.Minutes
	}
	for total > keepMinutes && len(h.Runs) > 0 {
		drop := min(total-keepMinutes, h.Runs[0].Minutes)
		h.Runs[0].Minutes -= drop
		total -= drop
		if h.Runs[0].Minutes == 0 {
			h.Runs = h.Runs[1:]
		}
	}
}

// Stats is a rep's uptime over a window.
type Stats struct {
	Tracked     bool
	Since       time.Time
	Day, Week   float64 // percent online; -1 if not tracked that long at all
	Month       float64
	TrackedMins int // minutes of history in the month window
	LastOutage  int // minutes since the end of the last outage, -1 if none in history
	OutageLen   int // length of that outage in minutes
	OfflineMins int // minutes offline in the month window
}

func (u *Uptime) Stats(addr string) Stats {
	u.mu.RLock()
	defer u.mu.RUnlock()
	h, ok := u.reps[addr]
	if !ok {
		return Stats{Day: -1, Week: -1, Month: -1, LastOutage: -1}
	}
	s := Stats{Tracked: true, Since: h.Since, LastOutage: -1}
	s.Day, _ = h.pct(1440)
	s.Week, _ = h.pct(7 * 1440)
	s.Month, s.TrackedMins = h.pct(30 * 1440)
	if s.Month >= 0 {
		s.OfflineMins = int(float64(s.TrackedMins)*(100-s.Month)/100 + 0.5)
	}
	ago := 0
	for i := len(h.Runs) - 1; i >= 0; i-- {
		r := h.Runs[i]
		if !r.Online {
			s.LastOutage, s.OutageLen = ago, r.Minutes
			break
		}
		ago += r.Minutes
	}
	return s
}

// pct is the online percentage over the newest window minutes (or all history if shorter).
func (h *history) pct(window int) (float64, int) {
	on, seen := 0, 0
	for i := len(h.Runs) - 1; i >= 0 && seen < window; i-- {
		n := min(h.Runs[i].Minutes, window-seen)
		if h.Runs[i].Online {
			on += n
		}
		seen += n
	}
	if seen == 0 {
		return -1, 0
	}
	return 100 * float64(on) / float64(seen), seen
}

// Save writes the history atomically.
func (u *Uptime) Save() error {
	if u.Path == "" {
		return nil
	}
	u.mu.RLock()
	b, err := json.Marshal(u.reps)
	u.mu.RUnlock()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(u.Path), 0o750); err != nil {
		return err
	}
	tmp := u.Path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o640); err != nil {
		return err
	}
	return os.Rename(tmp, u.Path)
}

// ImportSpyglass loads uptime files written by spyglass-api (one JSON file per
// rep, one ping per minute, run-length encoded). Reps we already track are
// left alone. It's a one-time migration aid; the explorer never needs spyglass.
func (u *Uptime) ImportSpyglass(dir string) (int, error) {
	files, err := filepath.Glob(filepath.Join(dir, "ban_*.json"))
	if err != nil {
		return 0, err
	}
	n := 0
	u.mu.Lock()
	defer u.mu.Unlock()
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			return n, err
		}
		var doc struct {
			Address string           `json:"address"`
			Start   int64            `json:"trackStartUnixTimestamp"`
			Pings   []map[string]int `json:"pingStats"`
		}
		if json.Unmarshal(b, &doc) != nil {
			continue // spyglass may be mid-write; skip rather than fail the import
		}
		if !strings.HasPrefix(doc.Address, "ban_") || u.reps[doc.Address] != nil {
			continue
		}
		h := &history{Since: time.UnixMilli(doc.Start)}
		for _, p := range doc.Pings {
			h.add(p["1"] > 0, p["1"]+p["0"])
		}
		h.trim()
		u.reps[doc.Address] = h
		n++
	}
	return n, nil
}

// Days returns the online percentage for each of the last n days, oldest
// first; -1 marks days before tracking started.
func (u *Uptime) Days(addr string, n int) []float64 {
	u.mu.RLock()
	defer u.mu.RUnlock()
	out := make([]float64, n)
	for i := range out {
		out[i] = -1
	}
	h, ok := u.reps[addr]
	if !ok {
		return out
	}
	on := make([]int, n)
	seen := make([]int, n)
	m := 0 // minutes back from now
	for i := len(h.Runs) - 1; i >= 0 && m < n*1440; i-- {
		r := h.Runs[i]
		for k := 0; k < r.Minutes && m < n*1440; k++ {
			d := n - 1 - m/1440
			seen[d]++
			if r.Online {
				on[d]++
			}
			m++
		}
	}
	for i := range out {
		if seen[i] > 0 {
			out[i] = 100 * float64(on[i]) / float64(seen[i])
		}
	}
	return out
}
