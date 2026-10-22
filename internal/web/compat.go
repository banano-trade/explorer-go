package web

import (
	"encoding/json"
	"math"
	"net/http"
	"time"

	"github.com/banano-trade/explorer-go/internal/ban"
)

// repScoreDTO matches spyglass-api's /banano/v1/representatives/scores so
// wallets built against spyglass (Nault) keep working.
type repScoreDTO struct {
	Address           string          `json:"address"`
	Alias             string          `json:"alias,omitempty"`
	Online            bool            `json:"online"`
	DaysAge           int             `json:"daysAge"`
	Principal         bool            `json:"principal"`
	Weight            float64         `json:"weight"`           // BAN
	WeightPercentage  float64         `json:"weightPercentage"` // of online weight
	Score             int             `json:"score"`            // 0-100, same rules as the rep score pages
	Grade             string          `json:"grade"`            // A-D or Offline
	UptimePercentages *uptimePercents `json:"uptimePercentages,omitempty"`
}

type uptimePercents struct {
	Day        float64 `json:"day"`
	Week       float64 `json:"week"`
	Month      float64 `json:"month"`
	SemiAnnual float64 `json:"semiAnnual"`
	Year       float64 `json:"year"`
}

func round2(f float64) float64 { return math.Round(f*100) / 100 }

// repScoresAPI serves every scored representative, biggest weight first.
func (s *Server) repScoresAPI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json")
	snap := s.snapshot(r.Context())
	if snap == nil {
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("[]\n"))
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=60")
	out := make([]repScoreDTO, 0, len(snap.By))
	reps := append(snap.Online[:len(snap.Online):len(snap.Online)], snap.Offline...)
	for _, rp := range reps {
		d := repScoreDTO{Address: rp.Account, Alias: rp.Name, Online: rp.Online, Principal: rp.Principal,
			Weight: math.Round(ban.Float(rp.Weight)), WeightPercentage: round2(rp.SharePct), Score: rp.Score.Points, Grade: rp.Score.Grade}
		if rp.Uptime.Tracked {
			d.DaysAge = int(time.Since(rp.Uptime.Since).Hours() / 24)
			u := s.Reps.Uptime
			d.UptimePercentages = &uptimePercents{Day: round2(rp.Uptime.Day), Week: round2(rp.Uptime.Week), Month: round2(rp.Uptime.Month),
				SemiAnnual: round2(u.Percent(rp.Account, 182)), Year: round2(u.Percent(rp.Account, 365))}
		}
		out = append(out, d)
	}
	enc := json.NewEncoder(w)
	_ = enc.Encode(out)
}
