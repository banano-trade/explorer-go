package monitors

import (
	"context"
	"io"
	"log/slog"
	"net"
	"os"
	"testing"

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
