package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/banano-trade/explorer-go/internal/ban"
	"github.com/banano-trade/explorer-go/internal/node"
)

var zeroHash = strings.Repeat("0", 64)

type blockData struct {
	Hash, Type            string
	Account, AccountName  string
	Party, PartyName      string // the other side of a send/receive
	Amount, Balance       string
	Height, AccountBlocks string
	Time                  int64
	Confirmed             bool
	Rep, RepName          string
	Previous, Next        string
	Match                 string // receive for a send, send for a receive
	Received              bool   // send only
	ReceivedUnknown       bool   // received, but by which block is unknown
	Burn                  bool   // sent to a burn address
	Signature, Work, Link string
	JSON                  string
	Latest                bool
}

func (s *Server) block(w http.ResponseWriter, r *http.Request) {
	raw := r.PathValue("hash")
	h := strings.ToUpper(strings.TrimSpace(raw))
	if !ban.IsHash(h) {
		s.render(w, r, http.StatusNotFound, "search", page{Title: "Not a block hash", Data: searchData{Query: raw, Problem: "nomatch"}})
		return
	}
	if h != raw {
		http.Redirect(w, r, "/block/"+h, http.StatusMovedPermanently)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	b, err := s.Node.BlockInfo(ctx, h)
	if errors.Is(err, node.ErrNotFound) {
		s.render(w, r, http.StatusNotFound, "search", page{Title: "Block not found", Data: searchData{Query: h, Problem: "noblock"}})
		return
	}
	if err != nil {
		s.nodeDown(w, r, err)
		return
	}
	c := b.Contents
	d := &blockData{
		Hash: h, Type: blockType(b.Subtype, c.Type), Account: b.BlockAccount, AccountName: s.displayName(b.BlockAccount),
		Amount: b.Amount, Balance: b.Balance, Height: b.Height, Time: atoi(b.LocalTimestamp), Confirmed: b.Confirmed == "true",
		Rep: c.Representative, RepName: s.displayName(c.Representative), Previous: c.Previous, Signature: c.Signature, Work: c.Work, Link: c.Link,
	}
	if d.Previous == zeroHash {
		d.Previous = ""
		if d.Type == "receive" {
			d.Type = "open"
		}
	}
	if b.Successor != zeroHash && b.Successor != "" {
		d.Next = b.Successor
	} else {
		d.Latest = true
	}
	switch d.Type {
	case "send":
		d.Party = c.LinkAsAccount
		if rh := b.ReceiveHash; rh != "" && rh != zeroHash {
			d.Match, d.Received = rh, true
		} else {
			// This node has no receive_hash; a confirmed send that is no longer
			// receivable has been received, we just don't know by which block yet.
			var ex struct {
				Exists string `json:"exists"`
			}
			if s.Node.Call(ctx, "receivable_exists", map[string]any{"hash": h}, &ex) == nil {
				d.Received = ex.Exists == "0"
				d.ReceivedUnknown = d.Received
			}
		}
	case "receive", "open":
		d.Party = b.SourceAccount
		d.Match = c.Link
		if d.Party == "" {
			if src, err := s.Node.BlockInfo(ctx, c.Link); err == nil {
				d.Party = src.BlockAccount
			}
		}
	}
	d.PartyName = s.displayName(d.Party)
	if a, ok := s.Known.Get(d.Party); ok && a.Category == "burn" && d.Type == "send" {
		d.Burn = true
	}
	if ai, err := s.Node.AccountInfo(ctx, b.BlockAccount); err == nil {
		d.AccountBlocks = ai.BlockCount
	}
	var full struct {
		Contents json.RawMessage `json:"contents"`
	}
	if json.Unmarshal(b.Raw, &full) == nil {
		var buf bytes.Buffer
		if json.Indent(&buf, full.Contents, "", "  ") == nil {
			d.JSON = buf.String()
		}
	}
	title := "Block " + ban.ShortHash(h)
	desc := "Banano " + d.Type + " block"
	if !isZero(d.Amount) && d.Type != "change" {
		desc += " of " + ban.FormatSmart(ban.Raw(d.Amount)) + " BAN"
	}
	s.render(w, r, http.StatusOK, "block", page{Title: title, Description: desc + ".", Data: d, Image: "/og/block/" + h + ".png"})
}
