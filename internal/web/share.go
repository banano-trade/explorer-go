package web

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"time"

	"github.com/banano-trade/explorer-go/internal/ban"
	"github.com/banano-trade/explorer-go/internal/node"
	"github.com/banano-trade/explorer-go/internal/og"
)

// ogImage serves /og/account/<addr>.png and /og/block/<hash>.png.
func (s *Server) ogImage(w http.ResponseWriter, r *http.Request) {
	kind, file := r.PathValue("kind"), r.PathValue("file")
	id := strings.TrimSuffix(file, ".png")
	if id == file {
		http.NotFound(w, r)
		return
	}
	switch kind {
	case "account":
		if _, err := ban.ParseAddress(id); err != nil || id != strings.ToLower(id) {
			http.NotFound(w, r)
			return
		}
	case "block":
		if !ban.IsHash(id) || id != strings.ToUpper(id) {
			http.NotFound(w, r)
			return
		}
	case "page":
		if !pageCards[id] {
			http.NotFound(w, r)
			return
		}
	default:
		http.NotFound(w, r)
		return
	}
	v, err := s.ogCache.get(r.Context(), kind+":"+id, 10*time.Minute, func(ctx context.Context) (any, error) { return s.drawCard(ctx, kind, id) })
	if errors.Is(err, node.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "couldn't draw image", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "public, max-age=600")
	_, _ = w.Write(v.([]byte))
}

// pageCards are the sections with their own share image (keyed by page.Nav).
var pageCards = map[string]bool{"home": true, "blocks": true, "reps": true, "network": true, "rich": true, "burn": true, "known": true, "health": true}

func (s *Server) drawCard(ctx context.Context, kind, id string) (any, error) {
	var card og.Card
	switch kind {
	case "page":
		card = s.pageCard(ctx, id)
	case "account":
		card = og.Card{Title: ban.Short(id), Mono: ban.Short(id)}
		if n := s.Known.Name(id); n != "" {
			card.Title = n
		}
		info, err := s.Node.AccountInfo(ctx, id)
		switch {
		case errors.Is(err, node.ErrNotFound):
			card.Big = "Not opened yet"
		case err != nil:
			return nil, err
		default:
			card.Big = ban.FormatSmart(ban.Raw(info.Balance)) + " BAN"
			card.Facts = append(card.Facts, ban.Group(info.BlockCount)+" blocks")
			if k, ok := s.Known.Get(id); ok {
				card.Facts = append(card.Facts, categoryLabel(k.Category))
			}
			rep := s.Known.Name(info.Representative)
			if rep == "" {
				rep = ban.Short(info.Representative)
			}
			if snap := s.snapshot(ctx); snap != nil && snap.By[info.Representative] != nil {
				rep += " · " + snap.By[info.Representative].Score.Grade
			}
			card.Facts = append(card.Facts, "Rep: "+rep)
		}
	case "block":
		h := strings.ToUpper(id)
		b, err := s.Node.BlockInfo(ctx, h)
		if err != nil {
			return nil, err
		}
		t := blockType(b.Subtype, b.Contents.Type)
		card.Kicker = strings.ToUpper(t[:1]) + t[1:] + " block"
		card.Mono = ban.ShortHash(h)
		who := s.Known.Name(b.BlockAccount)
		if who == "" {
			who = ban.Short(b.BlockAccount)
		}
		card.Title = who
		if t != "change" {
			card.Big = ban.FormatSmart(ban.Raw(b.Amount)) + " BAN"
		}
		if b.Confirmed == "true" {
			card.Facts = append(card.Facts, "Confirmed")
		}
		if ts := atoi(b.LocalTimestamp); ts > 0 {
			card.Facts = append(card.Facts, time.Unix(ts, 0).UTC().Format("2 Jan 2006, 15:04 UTC"))
		}
	}
	return og.PNG(card)
}

// pageCard builds a section's share image from live numbers.
func (s *Server) pageCard(ctx context.Context, nav string) og.Card {
	c := og.Card{}
	snap := s.snapshot(ctx)
	sc := s.ledgerScan()
	grp := func(n int) string { return ban.Group(fmt.Sprint(n)) }
	switch nav {
	case "home":
		c.Title = "Explore the Banano ledger"
		if bc, err := s.Node.BlockCount(ctx); err == nil {
			c.Big = ban.Group(bc.Count) + " blocks"
		}
		if snap != nil {
			c.Facts = append(c.Facts, fmt.Sprintf("%d reps online", len(snap.Online)))
		}
		if s.Live != nil {
			if st := s.Live.Stats(time.Hour); st.Count > 0 {
				c.Facts = append(c.Facts, fmt.Sprintf("confirms in %d ms", st.Median))
			}
		}
		if s.Health != nil {
			if r, ok := s.Health.Last(); ok && r.Good {
				c.Facts = append(c.Facts, "We good")
			}
		}
	case "blocks":
		c.Kicker, c.Title = "Live", "Latest blocks"
		if s.Live != nil {
			if st := s.Live.Stats(time.Hour); st.Count > 0 {
				c.Big = fmt.Sprintf("%d ms median confirm", st.Median)
				c.Facts = []string{fmt.Sprintf("95%% within %d ms", st.P95), fmt.Sprintf("~%d voters per block", st.Voters)}
			}
		}
	case "reps":
		c.Title = "Representatives"
		if snap != nil {
			c.Big = fmt.Sprintf("%d online · Nakamoto %d", len(snap.Online), snap.Nakamoto)
			c.Facts = []string{fmt.Sprintf("%d reps graded A", snap.CountGrade("A")), "Pick a good rep"}
		}
	case "network":
		c.Title = "Network"
		if snap != nil {
			c.Big = shortBan(snap.OnlineWeight) + " BAN online"
			c.Facts = []string{fmt.Sprintf("Nakamoto coefficient %d", snap.Nakamoto)}
		}
	case "rich":
		c.Title = "Rich list"
		if sc != nil {
			c.Big = grp(sc.Accounts) + " accounts with a balance"
		}
	case "burn":
		c.Kicker, c.Title = "Burned forever", "Banano burn"
		if sc != nil && sc.Burned != nil {
			c.Big = bigBanInt(sc.Burned) + " BAN"
			c.Facts = []string{fmt.Sprintf("%.1f%% of all BAN", ratio(sc.Burned, maxSupply)*100), grp(len(sc.Burners)) + " burners"}
		}
	case "known":
		c.Title = "Known accounts"
		c.Big = grp(len(s.Known.All())) + " named accounts"
		c.Facts = []string{"Exchanges, faucets, reps and more"}
	case "health":
		c.Kicker, c.Title = "WeGoodFam™", "Network health"
		if s.Health != nil {
			if r, ok := s.Health.Last(); ok {
				c.Title = r.Headline
				c.Facts = []string{r.Summary}
			}
		}
	}
	return c
}

func bigBanInt(raw *big.Int) string { return ban.Group(fmt.Sprintf("%.0f", ban.Float(raw))) }
