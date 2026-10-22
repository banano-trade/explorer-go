package web

import (
	"context"
	"errors"
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

func (s *Server) drawCard(ctx context.Context, kind, id string) (any, error) {
	var card og.Card
	switch kind {
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
