package web

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"time"

	"github.com/banano-trade/explorer-go/internal/ban"
	"github.com/banano-trade/explorer-go/internal/live"
	"github.com/banano-trade/explorer-go/internal/node"
)

// row is one confirmation ready for templates and the live stream.
type liveRow struct {
	Hash       string `json:"hash"`
	Type       string `json:"type"`
	Account    string `json:"account"`
	Name       string `json:"name,omitempty"`
	Short      string `json:"short"`
	Amount     string `json:"amount"` // formatted BAN
	Raw        string `json:"-"`
	Time       int64  `json:"time"` // unix seconds
	DurationMS int64  `json:"ms"`
	Slow       bool   `json:"slow,omitempty"`
}

const slowMS = 250

func (s *Server) liveRow(c live.Confirmation) liveRow {
	r := liveRow{Hash: c.Hash, Type: c.Type, Account: c.Account, Name: s.Known.Name(c.Account), Short: ban.Short(c.Account),
		Raw: c.Amount, Time: c.Time / 1000, DurationMS: c.DurationMS, Slow: c.DurationMS > slowMS}
	if c.Type != "change" && c.Type != "epoch" {
		r.Amount = ban.FormatSmart(ban.Raw(c.Amount))
	}
	return r
}

var (
	tenthBan = new(big.Int).Exp(big.NewInt(10), big.NewInt(28), nil)
	whale    = new(big.Int).Mul(big.NewInt(100000), new(big.Int).Exp(big.NewInt(10), big.NewInt(29), nil))
)

type blockFilter struct {
	Type  string // "", send, receive, change
	Small bool   // include amounts under 0.1 BAN
	Whale bool   // only 100K+ BAN
}

func parseFilter(r *http.Request) blockFilter {
	q := r.URL.Query()
	f := blockFilter{Small: q.Get("small") == "1", Whale: q.Get("whale") == "1"}
	switch t := q.Get("type"); t {
	case "send", "receive", "change":
		f.Type = t
	}
	return f
}

func (f blockFilter) keep(c live.Confirmation) bool {
	if f.Type != "" && c.Type != f.Type {
		return false
	}
	if c.Type == "change" || c.Type == "epoch" {
		return !f.Whale
	}
	a := ban.Raw(c.Amount)
	if f.Whale && a.Cmp(whale) < 0 {
		return false
	}
	return f.Small || a.Cmp(tenthBan) >= 0
}

// Query is the filter as a query string ("" or "?type=send&...").
func (f blockFilter) Query() string { return f.With("", "")[len("/blocks"):] }

// With builds the query string for this filter with one field changed.
func (f blockFilter) With(field, v string) string {
	g := f
	switch field {
	case "type":
		g.Type = v
	case "small":
		g.Small = v == "1"
	case "whale":
		g.Whale = v == "1"
	}
	q := ""
	add := func(k, v string) {
		if q == "" {
			q = "?"
		} else {
			q += "&"
		}
		q += k + "=" + v
	}
	if g.Type != "" {
		add("type", g.Type)
	}
	if g.Small {
		add("small", "1")
	}
	if g.Whale {
		add("whale", "1")
	}
	return "/blocks" + q
}

type blocksData struct {
	Blocks string
	Stats  live.Stats
	Rows   []liveRow
	F      blockFilter
	Live   bool
	Since  time.Time
}

func (s *Server) blocks(w http.ResponseWriter, r *http.Request) {
	d := &blocksData{F: parseFilter(r)}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if bc, err := s.Node.BlockCount(ctx); err == nil {
		d.Blocks = ban.Group(bc.Count)
	}
	if s.Live != nil {
		d.Live, _ = s.Live.Connected()
		d.Stats = s.Live.Stats(time.Hour)
		for _, c := range s.Live.Latest(100, d.F.keep) {
			d.Rows = append(d.Rows, s.liveRow(c))
		}
	}
	s.render(w, r, http.StatusOK, "blocks", page{Title: "Latest blocks", Nav: "blocks", Description: "Every block confirmed on the Banano network, live.", Data: d})
}

const maxStreams = 300

// liveStream sends confirmations to the browser as server-sent events.
func (s *Server) liveStream(w http.ResponseWriter, r *http.Request) {
	if s.Live == nil {
		http.Error(w, "live feed off", http.StatusServiceUnavailable)
		return
	}
	f := parseFilter(r)
	rc := http.NewResponseController(w)
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no") // nginx: don't buffer the stream
	ch, stop, ok := s.Live.Subscribe(maxStreams)
	defer stop()
	if !ok {
		h.Set("Content-Type", "text/plain; charset=utf-8")
		h.Set("Retry-After", "60")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("too many live viewers, try again soon\n"))
		return
	}
	w.WriteHeader(http.StatusOK)
	write := func(format string, a ...any) bool {
		_ = rc.SetWriteDeadline(time.Now().Add(15 * time.Second))
		if _, err := fmt.Fprintf(w, format, a...); err != nil {
			return false
		}
		return rc.Flush() == nil
	}
	if !write("retry: 5000\n\n") {
		return
	}
	beat := time.NewTicker(25 * time.Second)
	defer beat.Stop()
	end := time.After(30 * time.Minute) // let clients reconnect now and then
	for {
		select {
		case <-r.Context().Done():
			return
		case <-end:
			return
		case <-beat.C:
			if !write(": ping\n\n") {
				return
			}
		case c := <-ch:
			if !f.keep(c) {
				continue
			}
			b, _ := json.Marshal(s.liveRow(c))
			if !write("data: %s\n\n", b) {
				return
			}
		}
	}
}

// SeedLive fills the live feed from the node's last confirmed elections.
func (s *Server) SeedLive(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	ch, err := s.bg().ConfirmationHistory(ctx)
	if err != nil {
		return err
	}
	hashes := make([]string, 0, len(ch.Confirmations))
	for _, c := range ch.Confirmations {
		hashes = append(hashes, c.Hash)
	}
	infos, err := s.bg().BlocksInfo(ctx, hashes)
	if err != nil {
		return err
	}
	out := make([]live.Confirmation, 0, len(hashes))
	for _, c := range ch.Confirmations {
		b, ok := infos[c.Hash]
		if !ok || b.BlockAccount == "" {
			continue
		}
		lc := live.Confirmation{Hash: c.Hash, Account: b.BlockAccount, Type: blockType(b.Subtype, b.Contents.Type), Amount: b.Amount,
			Rep: b.Contents.Representative, Time: atoi(c.Time), DurationMS: atoi(c.Duration), Voters: int(atoi(c.Voters))}
		if lc.Type == "send" {
			lc.To = b.Contents.LinkAsAccount
		}
		out = append(out, lc)
	}
	s.Live.Seed(out)
	return nil
}

// FillLive keeps adding confirm times the websocket left out, until ctx ends.
func (s *Server) FillLive(ctx context.Context) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		ch, err := s.bg().ConfirmationHistory(cctx)
		cancel()
		if err != nil {
			continue
		}
		es := make(map[string]live.Election, len(ch.Confirmations))
		for _, c := range ch.Confirmations {
			es[c.Hash] = live.Election{DurationMS: atoi(c.Duration), Voters: int(atoi(c.Voters))}
		}
		s.Live.Fill(es)
	}
}

// bg is the node client for background work.
func (s *Server) bg() *node.Client {
	if s.Bg != nil {
		return s.Bg
	}
	return s.Node
}
