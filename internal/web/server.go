// Package web serves the explorer's HTML pages and small JSON endpoints.
package web

import (
	"context"
	"embed"
	"encoding/json"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/banano-trade/explorer-go/internal/ban"
	"github.com/banano-trade/explorer-go/internal/bns"
	"github.com/banano-trade/explorer-go/internal/health"
	"github.com/banano-trade/explorer-go/internal/insights"
	"github.com/banano-trade/explorer-go/internal/known"
	"github.com/banano-trade/explorer-go/internal/ledger"
	"github.com/banano-trade/explorer-go/internal/live"
	"github.com/banano-trade/explorer-go/internal/metrics"
	"github.com/banano-trade/explorer-go/internal/node"
	"github.com/banano-trade/explorer-go/internal/price"
	"github.com/banano-trade/explorer-go/internal/reps"
)

type Server struct {
	Node     *node.Client
	Known    *known.Store
	Log      *slog.Logger
	Static   fs.FS
	tmpl     map[string]*template.Template
	Price    func() float64 // USD per BAN, 0 when unknown
	Live     *live.Feed     // nil when the websocket feed is off
	Health   *health.Checker
	Reps     *reps.Directory
	BNS      *bns.Resolver // nil: no BNS names
	Bg       *node.Client  // background jobs' node client (nil: use Node)
	Ledger   *ledger.Scanner
	Metrics  *metrics.Sampler
	Insights *insights.Engine
	Prices   *price.Feed // nil when fiat is off
	BaseURL  string      // public origin, e.g. https://explorer.banano.trade

	exportSlot chan struct{}
	versions   map[string]string

	cache   *lru // pages' node results
	ogCache *lru // share images, kept apart so they can't push out node results
}

// New parses templates from tfs (layout.html plus one file per page).
func New(n *node.Client, k *known.Store, tfs fs.FS, static fs.FS, log *slog.Logger) (*Server, error) {
	s := &Server{Node: n, Known: k, Log: log, Static: static, cache: newLRU(5000), ogCache: newLRU(500)}
	s.Price = func() float64 { return s.Prices.Get("usd") }
	s.exportSlot = make(chan struct{}, 1)
	s.versions = assetVersions(static)
	pages, err := fs.Glob(tfs, "*.html")
	if err != nil {
		return nil, err
	}
	s.tmpl = map[string]*template.Template{}
	for _, p := range pages {
		if p == "layout.html" {
			continue
		}
		t, err := template.New("layout.html").Funcs(s.funcs()).ParseFS(tfs, "layout.html", p)
		if err != nil {
			return nil, err
		}
		s.tmpl[strings.TrimSuffix(p, ".html")] = t
	}
	return s, nil
}

// remember caches f's result for ttl under key (see lru).
func (s *Server) remember(ctx context.Context, key string, ttl time.Duration, f func(context.Context) (any, error)) (any, error) {
	return s.cache.get(ctx, key, ttl, f)
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /static/", http.StripPrefix("/static/", staticHandler(s.Static)))
	mux.HandleFunc("GET /{$}", s.home)
	mux.HandleFunc("GET /search", s.search)
	mux.HandleFunc("GET /blocks", s.blocks)
	mux.HandleFunc("GET /health", s.healthPage)
	mux.HandleFunc("GET /representatives", s.representatives)
	mux.HandleFunc("GET /network", s.network)
	mux.HandleFunc("GET /robots.txt", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		// Pages are welcome; the expensive variants of them are not.
		_, _ = w.Write([]byte("User-agent: *\nDisallow: /api/\nDisallow: /qr/\nDisallow: /search\nDisallow: /delegate/check\nDisallow: /*export.csv\nDisallow: /*?tab=insights\nDisallow: /*?head=\nDisallow: /*&head=\nDisallow: /*?height=\nDisallow: /*?type=\n"))
	})
	mux.HandleFunc("GET /qr/{file}", s.qrImage)
	mux.HandleFunc("GET /og/{kind}/{file}", s.ogImage)
	mux.HandleFunc("GET /menu", func(w http.ResponseWriter, r *http.Request) {
		s.render(w, r, http.StatusOK, "menu", page{Title: "Menu"})
	})
	mux.HandleFunc("GET /richlist", s.richlist)
	mux.HandleFunc("GET /burn", s.burn)
	mux.HandleFunc("GET /known", s.knownPage)
	mux.HandleFunc("GET /delegate", s.delegate)
	mux.HandleFunc("GET /delegate/check", s.delegateCheck)
	mux.HandleFunc("GET /rep/{addr}", s.repCard)
	mux.HandleFunc("GET /api/health", s.healthAPI)
	mux.HandleFunc("GET /api/live", s.liveStream)
	mux.HandleFunc("GET /api/suggest", s.suggest)
	mux.HandleFunc("GET /account/{addr}", s.account)
	mux.HandleFunc("GET /block/{hash}", s.block)
	mux.HandleFunc("GET /account/{addr}/export.csv", s.exportCSV)
	mux.HandleFunc("GET /hash/{hash}", func(w http.ResponseWriter, r *http.Request) { // old Creeper links
		h := strings.ToUpper(r.PathValue("hash"))
		if !ban.IsHash(h) {
			http.NotFound(w, r)
			return
		}
		http.Redirect(w, r, "/block/"+h, http.StatusMovedPermanently)
	})
	// Links from the old Creeper/Spyglass explorer keep working.
	moved := func(to string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, to, http.StatusMovedPermanently) }
	}
	for from, to := range map[string]string{"/known-accounts": "/known", "/wallets": "/richlist", "/bookmarks": "/", "/vanity": "/", "/monitor": "/representatives", "/status": "/health", "/node": "/network", "/explorer": "/"} {
		mux.HandleFunc("GET "+from, moved(to))
	}
	mux.HandleFunc("GET /explorer/{kind}/{id}/{rest...}", s.oldExplorer)
	mux.HandleFunc("GET /explorer/{kind}/{id}", s.oldExplorer)
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		s.render(w, r, http.StatusNotFound, "notfound", page{Title: "Not found", Data: map[string]any{"Query": r.URL.Path}})
	})
	return securityHeaders(compress(mux))
}

func staticHandler(f fs.FS) http.Handler {
	h := http.FileServer(http.FS(f))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "" || strings.HasSuffix(r.URL.Path, "/") { // no directory listings
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("v") != "" || strings.HasPrefix(r.URL.Path, "fonts/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "public, max-age=3600")
		}
		h.ServeHTTP(w, r)
	})
}

func securityHeaders(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hd := w.Header()
		hd.Set("X-Content-Type-Options", "nosniff")
		hd.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		hd.Set("X-Frame-Options", "DENY")
		hd.Set("Cross-Origin-Opener-Policy", "same-origin")
		hd.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=()")
		hd.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' https://monkey.banano.cc data:; style-src 'self' 'unsafe-inline'; script-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		h.ServeHTTP(w, r)
	})
}

// page is what every template gets.
type page struct {
	Title       string
	Description string
	Nav         string // which nav item is current
	Data        any
	Status      healthChip
	Prices      string // JSON of BAN prices for the fiat setting
	Canonical   string
	Image       string // og:image path
}

type healthChip struct {
	Good bool
	Text string
}

func (s *Server) render(w http.ResponseWriter, r *http.Request, code int, name string, p page) {
	t, ok := s.tmpl[name]
	if !ok {
		http.Error(w, "missing template "+name, http.StatusInternalServerError)
		return
	}
	if p.Image == "" && pageCards[p.Nav] && code == http.StatusOK {
		p.Image = "/og/page/" + p.Nav + ".png"
	}
	if s.BaseURL != "" {
		p.Canonical = s.BaseURL + r.URL.Path
		if p.Image != "" {
			p.Image = s.BaseURL + p.Image
		}
	}
	if pr := s.Prices.All(); len(pr) > 0 {
		b, _ := json.Marshal(pr)
		p.Prices = string(b)
	}
	if p.Status.Text == "" {
		p.Status = s.chip(r.Context())
	}
	var b strings.Builder
	if err := t.Execute(&b, p); err != nil {
		s.Log.Error("render", "page", name, "err", err)
		http.Error(w, "something went wrong", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(code)
	_, _ = w.Write([]byte(b.String()))
}

// chip is the WeGoodFam header status. Until the health checker exists it only
// checks that the node answers and has no big unconfirmed backlog.
func (s *Server) chip(ctx context.Context) healthChip {
	if s.Health != nil {
		if r, ok := s.Health.Last(); ok {
			switch {
			case r.NodeDown:
				return healthChip{Good: false, Text: "Node down"}
			case !r.Good:
				return healthChip{Good: false, Text: "Borked"}
			}
			return healthChip{Good: true, Text: "We good"}
		}
	}
	v, err := s.remember(ctx, "chip", 20*time.Second, func(ctx context.Context) (any, error) {
		ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		bc, err := s.Node.BlockCount(ctx)
		if err != nil {
			return healthChip{Good: false, Text: "Node down"}, nil
		}
		backlog := atoi(bc.Count) - atoi(bc.Cemented)
		if backlog >= 200 {
			return healthChip{Good: false, Text: "Borked"}, nil
		}
		return healthChip{Good: true, Text: "We good"}, nil
	})
	if err != nil {
		return healthChip{Good: false, Text: "Unknown"}
	}
	return v.(healthChip)
}

//go:embed assets
var assets embed.FS

// Assets returns the embedded templates and static files.
func Assets() (templates, static fs.FS) {
	t, _ := fs.Sub(assets, "assets/templates")
	st, _ := fs.Sub(assets, "assets/static")
	return t, st
}

// oldExplorer maps /explorer/account|block|hash|auto/<id> to the new pages.
func (s *Server) oldExplorer(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	switch {
	case ban.LooksLikeAddress(id):
		if _, err := ban.ParseAddress(id); err == nil {
			http.Redirect(w, r, "/account/"+strings.ToLower(id), http.StatusMovedPermanently)
			return
		}
	case ban.IsHash(strings.ToUpper(id)):
		http.Redirect(w, r, "/block/"+strings.ToUpper(id), http.StatusMovedPermanently)
		return
	}
	http.Redirect(w, r, "/search?q="+url.QueryEscape(id), http.StatusFound)
}

// displayName is an account's public name: its known-account alias, else its
// BNS name, else "".
func (s *Server) displayName(addr string) string {
	if n := s.Known.Name(addr); n != "" {
		return n
	}
	if s.BNS != nil {
		if ns := s.BNS.NamesFor(addr); len(ns) > 0 {
			return ns[0]
		}
	}
	return ""
}
