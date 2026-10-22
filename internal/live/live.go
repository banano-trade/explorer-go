// Package live follows the node's websocket for confirmed blocks. It keeps the
// most recent ones in memory, keeps hourly confirm-time stats on disk, and fans
// confirmations out to subscribers (the browser live feed).
package live

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// Confirmation is one confirmed block as reported by the node.
type Confirmation struct {
	Hash       string `json:"hash"`
	Account    string `json:"account"`
	Type       string `json:"type"` // send, receive, change, epoch
	Amount     string `json:"amount"`
	To         string `json:"to,omitempty"` // destination for sends
	Rep        string `json:"rep,omitempty"`
	Time       int64  `json:"time"`     // unix ms when our node confirmed it
	DurationMS int64  `json:"duration"` // election length on our node, 0 if unknown
	Voters     int    `json:"voters"`
	Sampled    bool   `json:"sampled,omitempty"` // confirmed while the vote stream was listening
}

// Hour holds confirm-time stats for one clock hour.
type Hour struct {
	Start  int64 `json:"start"` // unix seconds
	Count  int   `json:"count"`
	Median int64 `json:"median"`
	P95    int64 `json:"p95"`
	Max    int64 `json:"max"`
}

const (
	keepRecent = 2000
	keepHours  = 24 * 60 // 60 days
)

type Feed struct {
	URL  string
	Path string // snapshot file; empty disables saving
	Log  *slog.Logger

	mu        sync.RWMutex
	recent    []Confirmation // newest last
	hours     []Hour         // oldest first, closed hours only
	cur       []int64        // durations in the open hour
	curStart  int64
	connected bool
	lastMsg   time.Time
	since     time.Time // first confirmation seen, survives restarts
	subs      map[chan Confirmation]struct{}
	votes     map[string]*voteSet // recently confirmed block hash -> reps that voted
	pending   map[string]*voteSet // votes for blocks not (yet) confirmed, kept briefly
	known     map[string]bool     // hashes in recent
	listening time.Time           // since when the vote stream has been connected
}

type voteSet struct {
	first time.Time
	reps  map[string]struct{}
}

// Votes for unconfirmed blocks wait at most this long for the confirmation.
const pendingFor = 2 * time.Minute

const maxPending = 200000

func New(url, path string, log *slog.Logger) *Feed {
	f := &Feed{URL: url, Path: path, Log: log, subs: map[chan Confirmation]struct{}{}, votes: map[string]*voteSet{}, pending: map[string]*voteSet{}, known: map[string]bool{}}
	if path != "" {
		if err := f.load(); err != nil && !errors.Is(err, os.ErrNotExist) {
			log.Warn("live snapshot", "err", err)
		}
	}
	return f
}

// Run follows the websocket until ctx ends, reconnecting with backoff.
func (f *Feed) Run(ctx context.Context) {
	save := time.NewTicker(time.Minute)
	defer save.Stop()
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-save.C:
				f.saveLogged()
			}
		}
	}()
	wait := time.Second
	for ctx.Err() == nil {
		start := time.Now()
		err := f.follow(ctx)
		f.setConnected(false)
		if ctx.Err() != nil {
			break
		}
		if time.Since(start) > time.Minute {
			wait = time.Second
		}
		f.Log.Warn("websocket", "err", err, "retry", wait)
		select {
		case <-ctx.Done():
		case <-time.After(wait):
		}
		wait = min(wait*2, 30*time.Second)
	}
	f.saveLogged()
}

func (f *Feed) follow(ctx context.Context) error {
	dctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	c, _, err := websocket.Dial(dctx, f.URL, nil)
	cancel()
	if err != nil {
		return err
	}
	defer c.CloseNow()
	c.SetReadLimit(1 << 20)
	sub := `{"action":"subscribe","topic":"confirmation","ack":true,"options":{"include_election_info":true,"include_block":true}}`
	if err := c.Write(ctx, websocket.MessageText, []byte(sub)); err != nil {
		return err
	}
	// Every vote, replays and indeterminate ones included: a node's own votes
	// often arrive that way, and any vote for a block that confirmed counts.
	if err := c.Write(ctx, websocket.MessageText, []byte(`{"action":"subscribe","topic":"vote","options":{"include_replays":true,"include_indeterminate":true}}`)); err != nil {
		return err
	}
	f.setConnected(true)
	f.mu.Lock()
	f.listening = time.Now() // votes before this (and during any gap) were missed
	f.mu.Unlock()
	f.Log.Info("websocket connected", "url", f.URL)

	// The node is quiet when the network is; ping so a dead socket is noticed.
	pctx, pcancel := context.WithCancel(ctx)
	defer pcancel()
	go func() {
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-pctx.Done():
				return
			case <-t.C:
				tctx, tc := context.WithTimeout(pctx, 10*time.Second)
				err := c.Ping(tctx)
				tc()
				if err != nil {
					c.CloseNow()
					return
				}
			}
		}
	}()

	for {
		_, b, err := c.Read(ctx)
		if err != nil {
			return err
		}
		if rep, hashes, ok := parseVote(b); ok {
			f.addVote(rep, hashes)
		} else if cf, ok := parse(b); ok {
			f.add(cf)
		}
	}
}

type wsMsg struct {
	Topic   string `json:"topic"`
	Time    string `json:"time"`
	Message struct {
		Account  string `json:"account"`
		Amount   string `json:"amount"`
		Hash     string `json:"hash"`
		Election *struct {
			Duration string `json:"duration"`
			Time     string `json:"time"`
			Voters   string `json:"voters"`
		} `json:"election_info"`
		Block struct {
			Type           string `json:"type"`
			Subtype        string `json:"subtype"`
			Representative string `json:"representative"`
			LinkAsAccount  string `json:"link_as_account"`
		} `json:"block"`
	} `json:"message"`
}

func parse(b []byte) (Confirmation, bool) {
	var m wsMsg
	if json.Unmarshal(b, &m) != nil || m.Topic != "confirmation" || m.Message.Hash == "" {
		return Confirmation{}, false
	}
	msg := m.Message
	c := Confirmation{Hash: msg.Hash, Account: msg.Account, Amount: msg.Amount, Type: msg.Block.Subtype, Rep: msg.Block.Representative}
	if c.Type == "" {
		c.Type = msg.Block.Type
	}
	if c.Type == "open" {
		c.Type = "receive"
	}
	if c.Type == "send" {
		c.To = msg.Block.LinkAsAccount
	}
	c.Time, _ = strconv.ParseInt(m.Time, 10, 64)
	if e := msg.Election; e != nil {
		c.DurationMS, _ = strconv.ParseInt(e.Duration, 10, 64)
		c.Voters, _ = strconv.Atoi(e.Voters)
		if t, err := strconv.ParseInt(e.Time, 10, 64); err == nil && t > 0 {
			c.Time = t
		}
	}
	if c.Time == 0 {
		c.Time = time.Now().UnixMilli()
	}
	return c, true
}

func (f *Feed) add(c Confirmation) {
	f.mu.Lock()
	f.lastMsg = time.Now()
	if f.since.IsZero() {
		f.since = time.UnixMilli(c.Time)
	}
	c.Sampled = f.connected && !f.listening.IsZero() && time.Since(f.listening) > 10*time.Second
	f.recent = append(f.recent, c)
	f.known[c.Hash] = true
	if v := f.pending[c.Hash]; v != nil { // votes that arrived before the confirmation
		f.votes[c.Hash] = v
		delete(f.pending, c.Hash)
	}
	if len(f.recent) > keepRecent*2 {
		f.recent = append([]Confirmation(nil), f.recent[len(f.recent)-keepRecent:]...)
		f.reindex()
	}
	if c.DurationMS > 0 {
		hr := time.UnixMilli(c.Time).Truncate(time.Hour).Unix()
		if hr != f.curStart {
			f.closeHour()
			f.curStart = hr
		}
		f.cur = append(f.cur, c.DurationMS)
	}
	subs := make([]chan Confirmation, 0, len(f.subs))
	for ch := range f.subs {
		subs = append(subs, ch)
	}
	f.mu.Unlock()
	for _, ch := range subs {
		select {
		case ch <- c:
		default: // slow reader: skip rather than block the feed
		}
	}
}

// Seed merges older confirmations (from the node's confirmation_history) into
// the recent list, so the live pages aren't empty after a restart. Seeds don't
// count towards the hourly stats.
func (f *Feed) Seed(cs []Confirmation) {
	f.mu.Lock()
	defer f.mu.Unlock()
	seen := make(map[string]bool, len(f.recent))
	for _, c := range f.recent {
		seen[c.Hash] = true
	}
	all := append([]Confirmation(nil), f.recent...)
	for _, c := range cs {
		if !seen[c.Hash] {
			all = append(all, c)
			seen[c.Hash] = true
		}
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].Time < all[j].Time })
	if len(all) > keepRecent {
		all = all[len(all)-keepRecent:]
	}
	f.recent = all
	f.reindex()
}

// Election is what confirmation_history knows about one block.
type Election struct {
	DurationMS int64
	Voters     int
}

// Fill adds confirm times to recent blocks that came over the websocket without
// election info. It returns how many it filled.
func (f *Feed) Fill(es map[string]Election) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for i := range f.recent {
		c := &f.recent[i]
		if c.DurationMS > 0 {
			continue
		}
		e, ok := es[c.Hash]
		if !ok || e.DurationMS <= 0 {
			continue
		}
		c.DurationMS, c.Voters = e.DurationMS, e.Voters
		if time.UnixMilli(c.Time).Truncate(time.Hour).Unix() == f.curStart {
			f.cur = append(f.cur, e.DurationMS)
		}
		n++
	}
	return n
}

// closeHour folds the open hour into hours. Caller holds mu.
func (f *Feed) closeHour() {
	if len(f.cur) == 0 || f.curStart == 0 {
		f.cur = f.cur[:0]
		return
	}
	med, p95, mx := Percentiles(f.cur)
	f.hours = append(f.hours, Hour{Start: f.curStart, Count: len(f.cur), Median: med, P95: p95, Max: mx})
	if len(f.hours) > keepHours {
		f.hours = f.hours[len(f.hours)-keepHours:]
	}
	f.cur = f.cur[:0]
}

// Percentiles returns the median, 95th percentile and max of ds (which it sorts).
func Percentiles(ds []int64) (median, p95, max int64) {
	if len(ds) == 0 {
		return 0, 0, 0
	}
	s := append([]int64(nil), ds...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	i95 := (len(s)*95+99)/100 - 1
	return s[len(s)/2], s[i95], s[len(s)-1]
}

// Latest returns up to n confirmations, newest first, optionally filtered.
func (f *Feed) Latest(n int, keep func(Confirmation) bool) []Confirmation {
	f.mu.RLock()
	defer f.mu.RUnlock()
	out := make([]Confirmation, 0, n)
	for i := len(f.recent) - 1; i >= 0 && len(out) < n; i-- {
		if keep == nil || keep(f.recent[i]) {
			out = append(out, f.recent[i])
		}
	}
	return out
}

// Stats summarises confirm times over the last d.
type Stats struct {
	Count            int
	Median, P95, Max int64
	Avg              int64
	Voters           int // median voters per block
	Since            time.Time
}

func (f *Feed) Stats(d time.Duration) Stats {
	f.mu.RLock()
	cut := time.Now().Add(-d).UnixMilli()
	var ds []int64
	var vs []int64
	for i := len(f.recent) - 1; i >= 0 && f.recent[i].Time >= cut; i-- {
		if c := f.recent[i]; c.DurationMS > 0 {
			ds = append(ds, c.DurationMS)
			vs = append(vs, int64(c.Voters))
		}
	}
	since := f.since
	f.mu.RUnlock()
	s := Stats{Count: len(ds), Since: since}
	s.Median, s.P95, s.Max = Percentiles(ds)
	if len(ds) > 0 {
		var sum int64
		for _, d := range ds {
			sum += d
		}
		s.Avg = sum / int64(len(ds))
	}
	v, _, _ := Percentiles(vs)
	s.Voters = int(v)
	return s
}

// Hours returns closed hourly stats, oldest first.
func (f *Feed) Hours() []Hour {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return append([]Hour(nil), f.hours...)
}

// Healthy reports whether the websocket is up.
func (f *Feed) Connected() (bool, time.Time) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.connected, f.lastMsg
}

func (f *Feed) setConnected(v bool) {
	f.mu.Lock()
	f.connected = v
	f.mu.Unlock()
}

// Subscribe returns a channel of new confirmations and a function to stop.
// ok is false when max subscribers are already connected.
func (f *Feed) Subscribe(max int) (<-chan Confirmation, func(), bool) {
	ch := make(chan Confirmation, 64)
	f.mu.Lock()
	if max > 0 && len(f.subs) >= max {
		f.mu.Unlock()
		return nil, func() {}, false
	}
	f.subs[ch] = struct{}{}
	f.mu.Unlock()
	return ch, func() {
		f.mu.Lock()
		delete(f.subs, ch)
		f.mu.Unlock()
	}, true
}

// Subscribers counts open live streams.
func (f *Feed) Subscribers() int {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return len(f.subs)
}

type snapshot struct {
	Votes    map[string][]string `json:"votes,omitempty"` // sampled block -> reps that voted
	Recent   []Confirmation      `json:"recent"`
	Hours    []Hour              `json:"hours"`
	Cur      []int64             `json:"cur"`
	CurStart int64               `json:"cur_start"`
	Since    time.Time           `json:"since"`
}

func (f *Feed) saveLogged() {
	if f.Path == "" {
		return
	}
	if err := f.save(); err != nil {
		f.Log.Warn("live save", "err", err)
	}
}

func (f *Feed) save() error {
	f.mu.RLock()
	r := f.recent
	if len(r) > keepRecent {
		r = r[len(r)-keepRecent:]
	}
	votes := map[string][]string{}
	for _, c := range r {
		if v := f.votes[c.Hash]; c.Sampled && v != nil {
			for rep := range v.reps {
				votes[c.Hash] = append(votes[c.Hash], rep)
			}
		}
	}
	b, err := json.Marshal(snapshot{Votes: votes, Recent: r, Hours: f.hours, Cur: f.cur, CurStart: f.curStart, Since: f.since})
	f.mu.RUnlock()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(f.Path), 0o750); err != nil {
		return err
	}
	tmp := f.Path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o640); err != nil {
		return err
	}
	return os.Rename(tmp, f.Path)
}

func (f *Feed) load() error {
	b, err := os.ReadFile(f.Path)
	if err != nil {
		return err
	}
	var s snapshot
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	f.mu.Lock()
	f.recent, f.hours, f.cur, f.curStart, f.since = s.Recent, s.Hours, s.Cur, s.CurStart, s.Since
	f.reindex()
	for h, reps := range s.Votes {
		if f.known[h] {
			v := &voteSet{first: time.Now(), reps: map[string]struct{}{}}
			for _, r := range reps {
				v.reps[r] = struct{}{}
			}
			f.votes[h] = v
		}
	}
	f.mu.Unlock()
	return nil
}

var voteTopic = []byte(`"topic": "vote"`)

// parseVote reads a vote message; it avoids a full decode for other topics.
func parseVote(b []byte) (rep string, hashes []string, ok bool) {
	if !bytes.Contains(b, voteTopic) && !bytes.Contains(b, []byte(`"topic":"vote"`)) {
		return "", nil, false
	}
	var m struct {
		Topic   string `json:"topic"`
		Message struct {
			Account string   `json:"account"`
			Blocks  []string `json:"blocks"`
			Type    string   `json:"type"`
		} `json:"message"`
	}
	if json.Unmarshal(b, &m) != nil || m.Topic != "vote" {
		return "", nil, false
	}
	return m.Message.Account, m.Message.Blocks, true
}

func (f *Feed) addVote(rep string, hashes []string) {
	now := time.Now()
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, h := range hashes {
		m := f.pending
		if f.known[h] {
			m = f.votes
		}
		v := m[h]
		if v == nil {
			v = &voteSet{first: now, reps: map[string]struct{}{}}
			m[h] = v
		}
		v.reps[rep] = struct{}{}
	}
	if len(f.pending) > maxPending {
		for h, v := range f.pending {
			if now.Sub(v.first) > pendingFor {
				delete(f.pending, h)
			}
		}
		if len(f.pending) > maxPending { // a flood of fresh hashes: drop the waiting ones only
			f.pending = map[string]*voteSet{}
		}
	}
}

// reindex rebuilds the set of recent hashes and drops votes for blocks that
// left it. Caller holds mu.
func (f *Feed) reindex() {
	f.known = make(map[string]bool, len(f.recent))
	for _, c := range f.recent {
		f.known[c.Hash] = true
	}
	for h := range f.votes {
		if !f.known[h] {
			delete(f.votes, h)
		}
	}
}

// VoteShare reports, over recently confirmed blocks that saw any votes, how
// many each rep voted on. Blocks younger than 30 s are skipped so late votes
// have time to arrive.
func (f *Feed) VoteShare() (blocks int, byRep map[string]int) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	byRep = map[string]int{}
	cut := time.Now().Add(-30 * time.Second).UnixMilli()
	// Only blocks confirmed while we were listening: we saw all their votes.
	for i := len(f.recent) - 1; i >= 0; i-- {
		c := f.recent[i]
		if !c.Sampled || c.Time > cut {
			continue
		}
		v := f.votes[c.Hash]
		if v == nil || len(v.reps) == 0 {
			continue
		}
		blocks++
		for r := range v.reps {
			byRep[r]++
		}
	}
	return blocks, byRep
}
