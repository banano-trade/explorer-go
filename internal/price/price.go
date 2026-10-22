// Package price fetches the BAN price in a few currencies. It's optional: when
// it's off or the source is down, the site simply hides fiat amounts.
package price

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

// Currencies the settings offer.
var Currencies = []string{"usd", "eur", "try", "gbp", "btc"}

type Feed struct {
	URL string // CoinGecko simple-price endpoint
	Log *slog.Logger

	mu     sync.RWMutex
	prices map[string]float64
	at     time.Time
}

func New(log *slog.Logger) *Feed {
	return &Feed{URL: "https://api.coingecko.com/api/v3/simple/price?ids=banano&vs_currencies=usd,eur,try,gbp,btc", Log: log}
}

// Get returns BAN's price in cur, or 0 when unknown or older than an hour.
func (f *Feed) Get(cur string) float64 {
	if f == nil {
		return 0
	}
	f.mu.RLock()
	defer f.mu.RUnlock()
	if time.Since(f.at) > time.Hour {
		return 0
	}
	return f.prices[cur]
}

// All returns every known price (empty when stale).
func (f *Feed) All() map[string]float64 {
	out := map[string]float64{}
	if f == nil {
		return out
	}
	f.mu.RLock()
	defer f.mu.RUnlock()
	if time.Since(f.at) > time.Hour {
		return out
	}
	for k, v := range f.prices {
		out[k] = v
	}
	return out
}

func (f *Feed) Run(ctx context.Context) {
	for {
		if err := f.fetch(ctx); err != nil && ctx.Err() == nil {
			f.Log.Warn("price", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(10 * time.Minute):
		}
	}
}

func (f *Feed) fetch(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.URL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var r map[string]map[string]float64
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&r); err != nil {
		return err
	}
	if len(r["banano"]) == 0 {
		return nil
	}
	f.mu.Lock()
	f.prices, f.at = r["banano"], time.Now()
	f.mu.Unlock()
	return nil
}
