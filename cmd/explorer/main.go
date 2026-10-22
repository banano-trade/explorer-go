// Command explorer serves the Banano block explorer.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

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
	"github.com/banano-trade/explorer-go/internal/web"
)

var version = "dev"

func main() {
	listen := flag.String("listen", "127.0.0.1:9990", "HTTP listen address")
	nodeURL := flag.String("node", "http://[::1]:7072", "Banano node RPC URL")
	knownDir := flag.String("known", "", "directory with known-account JSON files (default: the list built in)")
	wsURL := flag.String("ws", "ws://[::1]:7079", "Banano node websocket URL (empty to disable the live feed)")
	importUptime := flag.String("import-uptime", "", "one-time: import spyglass-api rep-uptime files from this directory into -state, then exit")
	fiat := flag.Bool("fiat", true, "show fiat prices (fetched from CoinGecko every 10 minutes; off = the node is the only data source)")
	publicURL := flag.String("public-url", "", "public origin for share links and images, e.g. https://explorer.banano.trade")
	stateDir := flag.String("state", "data/state", "directory for files the explorer writes")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println("explorer", version)
		return
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	k, err := known.Load(*knownDir)
	if err != nil {
		log.Error("known accounts", "err", err)
		os.Exit(1)
	}
	tfs, static := web.Assets()
	// Visitors' pages and background jobs get separate slots on the node, so
	// a burst of traffic can't stall the jobs and vice versa.
	bg := node.NewLimited(*nodeURL, 4, 2*time.Minute)
	srv, err := web.New(node.New(*nodeURL), k, tfs, static, log)
	if err != nil {
		log.Error("templates", "err", err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	liveDone := make(chan struct{})
	if *wsURL != "" {
		srv.Live = live.New(*wsURL, filepath.Join(*stateDir, "live.json"), log)
		go func() {
			if err := srv.SeedLive(ctx); err != nil {
				log.Warn("seed live feed", "err", err)
			}
			srv.FillLive(ctx)
		}()
		go func() { srv.Live.Run(ctx); close(liveDone) }()
	} else {
		close(liveDone)
	}
	up, err := reps.NewUptime(filepath.Join(*stateDir, "uptime.json"))
	if err != nil {
		log.Error("uptime history", "err", err)
		os.Exit(1)
	}
	if *importUptime != "" {
		n, err := up.ImportSpyglass(*importUptime)
		if err != nil {
			log.Error("import uptime", "err", err)
			os.Exit(1)
		}
		if err := up.Save(); err != nil {
			log.Error("save uptime", "err", err)
			os.Exit(1)
		}
		log.Info("imported spyglass uptime; exiting", "reps", n, "file", up.Path)
		return
	}
	srv.Reps = &reps.Directory{Node: bg, Live: srv.Live, Uptime: up, Name: k.Name, Log: log}
	repsDone := make(chan struct{})
	go func() { srv.Reps.Run(ctx); close(repsDone) }()
	var burnAddrs []string
	for _, a := range k.All() {
		if a.Category == "burn" {
			burnAddrs = append(burnAddrs, a.Address)
		}
	}
	srv.Ledger = &ledger.Scanner{Node: bg, URL: *nodeURL, Dir: filepath.Join(*stateDir, "balances"), BurnAddrs: burnAddrs, Log: log}
	go srv.Ledger.Run(ctx)
	srv.Metrics = metrics.NewSampler(bg, filepath.Join(*stateDir, "metrics.json"), log)
	metricsDone := make(chan struct{})
	go func() { srv.Metrics.Run(ctx); close(metricsDone) }()
	if *fiat {
		srv.Prices = price.New(log)
		go srv.Prices.Run(ctx)
	}
	srv.BaseURL = strings.TrimRight(*publicURL, "/")
	srv.Insights = insights.NewEngine(bg, log)
	go srv.Insights.Run(ctx)
	srv.Bg = bg
	srv.BNS = &bns.Resolver{Node: bg, Log: log}
	go srv.BNS.Run(ctx)
	srv.Health = health.NewChecker(bg, srv.Live, log)
	go srv.Health.Run(ctx)
	hs := &http.Server{Addr: *listen, Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 60 * time.Second, IdleTimeout: 120 * time.Second, MaxHeaderBytes: 16 << 10,
		BaseContext: func(net.Listener) context.Context { return ctx }} // ends live streams on shutdown

	go func() {
		log.Info("listening", "addr", *listen, "node", *nodeURL, "version", version, "known", len(k.All()))
		if err := hs.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("server", "err", err)
			stop()
		}
	}()
	<-ctx.Done()
	sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = hs.Shutdown(sctx)
	<-liveDone // saves the live snapshot
	<-repsDone // saves uptime history
}
