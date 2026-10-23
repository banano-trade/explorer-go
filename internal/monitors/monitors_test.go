package monitors

import (
	"context"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/banano-trade/explorer-go/internal/node"
)

func TestCleanName(t *testing.T) {
	for in, want := range map[string]string{
		"  Mesharound ":               "Mesharound",
		"https://node.jungletv.live/": "node.jungletv.live",
		"a\x00b<script>":              "abscript",
		"multi   space\tname":         "multi space name",
	} {
		if got := cleanName(in); got != want {
			t.Errorf("cleanName(%q) = %q, want %q", in, got, want)
		}
	}
	if len([]rune(cleanName(string(make([]rune, 0))+"xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"))) != 40 {
		t.Error("not capped at 40")
	}
}

func TestPublic(t *testing.T) {
	for ip, want := range map[string]bool{"45.74.35.236": true, "127.0.0.1": false, "10.0.0.5": false, "192.168.1.1": false, "::1": false, "172.17.0.2": false} {
		if public(net.ParseIP(ip)) != want {
			t.Errorf("public(%s) != %v", ip, want)
		}
	}
}

// TestLive: NODE_URL='http://[::1]:7072' MONITORS=1 go test -v -run Live ./internal/monitors
func TestLive(t *testing.T) {
	if os.Getenv("NODE_URL") == "" || os.Getenv("MONITORS") == "" {
		t.Skip("set NODE_URL and MONITORS")
	}
	r := &Reader{Node: node.New(os.Getenv("NODE_URL")), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	r.refresh(context.Background())
	m, ok := r.Get("ban_1meshar4yyq7du6u6oc7yxpuj7b9geqnhtn9sxqu6yue4zopaimidhthm4eq")
	t.Logf("found %d monitors; Mesharound: %+v %v", len(r.byAcc), m, ok)
	if !ok {
		t.Fatal("Mesharound's monitor not found")
	}
}

func TestSaveLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "monitors.json")
	r := &Reader{Path: path}
	a := "ban_1meshar4yyq7du6u6oc7yxpuj7b9geqnhtn9sxqu6yue4zopaimidhthm4eq"
	in := map[string]Monitor{a: {Account: a, Name: "Mesharound", URL: "http://45.74.35.236", Seen: time.Now()},
		"ban_bogus": {Account: "ban_bogus", Name: "x"}}
	if err := r.save(in); err != nil {
		t.Fatal(err)
	}
	g := &Reader{Path: path}
	if err := g.Load(); err != nil {
		t.Fatal(err)
	}
	if g.Name(a) != "Mesharound" || g.Name("ban_bogus") != "" {
		t.Fatalf("got %q / %q", g.Name(a), g.Name("ban_bogus"))
	}
	if err := (&Reader{Path: filepath.Join(t.TempDir(), "missing.json")}).Load(); err != nil {
		t.Fatal("missing file should be fine:", err)
	}
}
