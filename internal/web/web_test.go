package web

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/banano-trade/explorer-go/internal/bns"
	"github.com/banano-trade/explorer-go/internal/health"
	"github.com/banano-trade/explorer-go/internal/insights"
	"github.com/banano-trade/explorer-go/internal/known"
	"github.com/banano-trade/explorer-go/internal/node"
	"github.com/banano-trade/explorer-go/internal/reps"
)

// TestPagesLive renders every page against a real node. Run with
// NODE_URL='http://[::1]:7072' go test ./internal/web
func TestPagesLive(t *testing.T) {
	url := os.Getenv("NODE_URL")
	if url == "" {
		t.Skip("NODE_URL not set")
	}
	k, err := known.Load("")
	if err != nil {
		t.Fatal(err)
	}
	tfs, static := Assets()
	s, err := New(node.New(url), k, tfs, static, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	s.Health = health.NewChecker(s.Node, nil, s.Log)
	up, _ := reps.NewUptime("")
	s.Insights = insights.NewEngine(s.Node, s.Log)
	s.BNS = &bns.Resolver{Node: s.Node, Log: s.Log}
	s.Reps = &reps.Directory{Node: s.Node, Uptime: up, Name: k.Name, Log: s.Log}
	if err := s.Reps.Refresh(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	s.Health.Once(context.Background())
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	jungle := "ban_1jung1eb3uomk1gsx7w6w7toqrikxm5pgn5wbsg5fpy96ckpdf6wmiuuzpca"
	cases := []struct {
		path string
		code int
		want string
	}{
		{"/", 200, "Live confirmations"},
		{"/account/" + jungle, 200, "JungleTV"},
		{"/account/" + jungle + "?tab=receivable", 200, "Receivable"},
		{"/account/ban_19potasho7ozny8r1drz3u3hb3r97fw4ndm4hegdsdzzns1c3nobdastcgaa?tab=delegators", 200, "Delegator"},
		{"/account/" + strings.ToUpper(jungle), 301, ""},
		{"/account/" + jungle[:63] + "b", 404, "typo"},
		{"/account/nano_1jung", 404, "Nano address"},
		{"/block/73FDC8927A4BEF21CA369624C3498B4C095CE51933F05269D51E78A0B6CA14FB", 200, "JungleTV Rain"},
		{"/block/C8A47F7E945448E975E237E5A666AAAB46FF130A8E2CAAF33B38EB300307B980", 200, "Burned forever"},
		{"/block/" + strings.Repeat("A", 64), 404, "never seen"},
		{"/hash/73FDC8927A4BEF21CA369624C3498B4C095CE51933F05269D51E78A0B6CA14FB", 301, ""},
		{"/search?q=" + jungle, 302, ""},
		{"/search?q=jungle", 200, "JungleTV"},
		{"/search?q=zzzzqqq", 404, "Nothing found"},
		{"/api/suggest?q=jung", 200, "JungleTV"},
		{"/static/app.js", 200, "use strict"},
		{"/nope", 404, "doesn't exist"},
		{"/blocks", 200, "Latest blocks"},
		{"/health", 200, "WeGoodFam"},
		{"/api/health", 200, "quorum_ratio"},
		{"/representatives", 200, "Kalium"},
		{"/delegate", 200, "How the score works"},
		{"/rep/ban_1ka1ium4pfue3uxtntqsrib8mumxgazsjf58gidh1xeo5te3whsq8z476goo", 200, "Where the points went"},
		{"/delegate/check?addr=" + jungle, 302, ""},
		{"/account/" + jungle, 200, "Rep score"},
		{"/account/" + jungle + "?tab=insights", 200, "Working it out"},
		{"/og/account/" + jungle + ".png", 200, "PNG"},
		{"/og/block/73FDC8927A4BEF21CA369624C3498B4C095CE51933F05269D51E78A0B6CA14FB.png", 200, "PNG"},
		{"/og/account/nope.png", 404, ""},
		{"/qr/" + jungle + ".svg?amount=1000", 200, "<svg"},
		{"/qr/" + jungle + ".svg?amount=-1", 400, ""},
		{"/menu", 200, "Known accounts"},
		{"/search?q=yusuf.ban", 302, ""},
		{"/search?q=Yusuf.BAN", 302, ""},
		{"/search?q=surelynobodyregisteredthis.ban", 404, "Nobody has registered"},
		// security regressions
		{"/account/ban_1ka1ium4pfue3uxtntqsrib8mumxgazsjf58gidh1xeo5te3whsq8z476goo?head=73FDC8927A4BEF21CA369624C3498B4C095CE51933F05269D51E78A0B6CA14FB", 302, ""},
		{"/og/account/" + strings.ToUpper(jungle) + ".png", 404, ""},
		{"/og/block/73fdc8927a4bef21ca369624c3498b4c095ce51933f05269d51e78a0b6ca14fb.png", 404, ""},
		{"/hash/evil.com", 404, ""},
		{"/static/", 404, ""},
		{"/account/" + jungle + "?tab=delegators", 200, "ledger scan"},
		{"/network", 200, "Nakamoto"},
		{"/known", 200, "Kalium"},
		{"/richlist", 200, "Still counting"},
		{"/burn", 200, "Still counting"},
	}
	for _, c := range cases {
		resp, err := client.Get(srv.URL + c.path)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != c.code {
			t.Errorf("%s: status %d, want %d", c.path, resp.StatusCode, c.code)
		}
		if c.want != "" && !strings.Contains(string(b), c.want) {
			t.Errorf("%s: body missing %q", c.path, c.want)
		}
	}
}
