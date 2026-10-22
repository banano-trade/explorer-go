package health

import (
	"math/big"
	"testing"
	"time"

	"github.com/banano-trade/explorer-go/internal/ban"
)

func base(now time.Time) Inputs {
	return Inputs{
		Now: now, Count: 1000, Cemented: 1000, MedianMS: 120, Samples: 50, PeerCemented: 1000,
		Online: ban.Raw("148643030951712622924426848643881565723"), Minimum: ban.Raw("90000000000000000000000000000000000000"),
		Delegated:    ban.Raw("180000000000000000000000000000000000000"),
		OnlineShares: []float64{0.258, 0.158, 0.113, 0.057, 0.045, 0.04, 0.04, 0.04, 0.04, 0.04, 0.04, 0.04, 0.04, 0.045},
		Peers:        47, LatestVersion: "26.1", OnLatest: 0.85,
	}
}

func TestAllGood(t *testing.T) {
	r := evaluate(base(time.Now()))
	if !r.Good || r.Headline != "We good, fam." {
		t.Fatalf("%+v", r)
	}
	if len(r.Checks) != 10 {
		t.Fatalf("%d checks", len(r.Checks))
	}
	// Nakamoto 6-ish and stall risk 2 are notes, not problems.
	if len(r.Problems) != 0 || len(r.Notes) != 2 {
		t.Fatalf("problems %v notes %v", r.Problems, r.Notes)
	}
	if r.Quorum < 1.6 || r.Quorum > 1.7 {
		t.Fatalf("quorum %v", r.Quorum)
	}
}

func TestBacklog(t *testing.T) {
	now := time.Now()
	in := base(now)
	in.Count = 1412
	in.BacklogSince = now.Add(-5 * time.Minute)
	if r := evaluate(in); !r.Good {
		t.Fatal("5 minutes of backlog should only warn")
	}
	in.BacklogSince = now.Add(-17 * time.Minute)
	r := evaluate(in)
	if r.Good || r.Headline != "Nope, borked." || r.Backlog != 412 {
		t.Fatalf("%+v", r)
	}
}

func TestQuorumAndSpeed(t *testing.T) {
	in := base(time.Now())
	in.Online = new(big.Int).Div(in.Minimum, big.NewInt(2))
	if evaluate(in).Good {
		t.Fatal("below minimum online weight should be borked")
	}
	in = base(time.Now())
	in.MedianMS = 12000
	if evaluate(in).Good {
		t.Fatal("12 s median should be borked")
	}
	in.MedianMS = 1500
	if r := evaluate(in); !r.Good || r.Headline != "We good, mostly." {
		t.Fatalf("%+v", r)
	}
}

func TestNewer(t *testing.T) {
	if !newer("26.1", "25.9") || newer("25.10", "26.0") || !newer("25.10", "25.9") {
		t.Fatal("version compare")
	}
}
