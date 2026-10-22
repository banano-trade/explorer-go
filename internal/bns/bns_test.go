package bns

import (
	"context"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/banano-trade/explorer-go/internal/ban"
	"github.com/banano-trade/explorer-go/internal/node"
)

func TestNames(t *testing.T) {
	if got := decodeName(ban.Address(EncodeName("yusuf"))); got != "yusuf" {
		t.Fatalf("round trip: %q", got)
	}
	for q, ok := range map[string]bool{"yusuf.ban": true, "Yusuf.BAN": true, "x.jtv": true, "a.b.ban": false, ".ban": false, "yusuf.com": false, "yusuf": false} {
		if _, _, got := Split(q); got != ok {
			t.Errorf("Split(%q) = %v", q, got)
		}
	}
}

// TestResolveLive: NODE_URL='http://[::1]:7072' go test -v -run Live ./internal/bns
func TestResolveLive(t *testing.T) {
	url := os.Getenv("NODE_URL")
	if url == "" {
		t.Skip("NODE_URL not set")
	}
	r := &Resolver{Node: node.New(url), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	ctx := context.Background()
	iss, err := r.issued(ctx, "ban")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf(".ban names issued: %d", len(iss))
	n := 0
	for name := range iss {
		_ = iss[name].to
		if n++; n > 5 {
			break
		}
		d, err := r.Resolve(ctx, name, "ban")
		t.Logf("%s -> resolves %q owner %s frozen=%v burned=%v err=%v", d.Full(), d.Resolves, ban.Short(d.Owner), d.Frozen, d.Burned, err)
	}
	d, err := r.Resolve(ctx, "yusuf", "ban")
	t.Logf("yusuf.ban -> %+v err=%v", d, err)
}

func TestIndexLive(t *testing.T) {
	url := os.Getenv("NODE_URL")
	if url == "" {
		t.Skip("NODE_URL not set")
	}
	r := &Resolver{Node: node.New(url), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	ctx, cancel := context.WithCancel(context.Background())
	go r.Run(ctx)
	for i := 0; i < 600 && r.indexedAt().IsZero(); i++ {
		time.Sleep(100 * time.Millisecond)
	}
	cancel()
	ns := r.NamesFor("ban_1tfjc4azwx844t9ccg7pz6zmdqtcskgzfru7hsga4pyej1ugkhiw39auhswa")
	t.Logf("names for yusuf's address: %v; total %d", ns, len(r.names))
	if !contains(ns, "yusuf.ban") {
		t.Fatal("yusuf.ban not indexed")
	}
	if got := r.Search("yus", 5); len(got) == 0 {
		t.Fatal("search found nothing")
	}
}
