package ledger

import (
	"context"
	"io"
	"log/slog"
	"os"
	"testing"

	"github.com/banano-trade/explorer-go/internal/node"
)

func TestDiff(t *testing.T) {
	old := daily{"a": 5000, "b": 100, "gone": 2000}
	cur := daily{"a": 1000, "b": 90000, "new": 50}
	m := diff(old, cur)
	if len(m) != 3 || m[0].Account != "b" || m[1].Change != -4000 || m[2].Change != -2000 {
		t.Fatalf("%+v", m)
	}
}

// TestScanLive does a full scan: LEDGER_SCAN=1 NODE_URL='http://[::1]:7072' go test ./internal/ledger
func TestScanLive(t *testing.T) {
	url := os.Getenv("NODE_URL")
	if url == "" || os.Getenv("LEDGER_SCAN") == "" {
		t.Skip("set NODE_URL and LEDGER_SCAN")
	}
	s := &Scanner{Node: node.New(url), URL: url, Dir: t.TempDir(), Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		BurnAddrs: []string{"ban_1burnbabyburndiscoinferno111111111111111111111111111aj49sw3w"}}
	sc, err := s.Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("accounts %d, top %s %s, took %s, burned %s in %d burns, %d burners", sc.Accounts, sc.Top[0].Account, sc.Top[0].Balance, sc.Took, sc.Burned, sc.BurnCount, len(sc.Burners))
	if sc.Accounts < 100000 || len(sc.Top) != keepTop || sc.BurnCount < 1000 {
		t.Fatal("scan looks short")
	}
	for i := 1; i < len(sc.Top); i++ {
		if sc.Top[i].Balance.Cmp(sc.Top[i-1].Balance) > 0 {
			t.Fatal("top not sorted")
		}
	}
	t.Log(sc.Buckets)
}
