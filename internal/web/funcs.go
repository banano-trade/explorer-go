package web

import (
	"fmt"
	"html/template"
	"math/big"
	"strconv"
	"strings"
	"time"

	"github.com/banano-trade/explorer-go/internal/ban"
)

func atoi(s string) int64 {
	n, _ := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	return n
}

// ago renders a unix timestamp as "3 min ago". Zero means unknown.
func ago(ts int64) string {
	if ts <= 0 {
		return "unknown"
	}
	d := time.Since(time.Unix(ts, 0))
	switch {
	case d < 0:
		return "just now"
	case d < time.Minute:
		return fmt.Sprintf("%d s ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%d min ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%d h ago", int(d.Hours()))
	case d < 60*24*time.Hour:
		return fmt.Sprintf("%d days ago", int(d.Hours()/24))
	case d < 2*365*24*time.Hour:
		return fmt.Sprintf("%d mo ago", int(d.Hours()/24/30))
	}
	return fmt.Sprintf("%d y ago", int(d.Hours()/24/365))
}

// when renders a unix timestamp as an absolute UTC time; the browser rewrites it
// to the visitor's zone if they prefer (see app.js).
func when(ts int64) string {
	if ts <= 0 {
		return "unknown"
	}
	return time.Unix(ts, 0).UTC().Format("2 Jan 2006, 15:04 UTC")
}

func (s *Server) funcs() template.FuncMap {
	return template.FuncMap{
		"short":     ban.Short,
		"shortHash": ban.ShortHash,
		"group":     func(v any) string { return ban.Group(fmt.Sprint(v)) },
		"ban": func(raw string) string { // 13,820.25
			return ban.FormatSmart(ban.Raw(raw))
		},
		"banFull": func(raw string) string { return ban.Format(ban.Raw(raw), 29) },
		"banParts": func(raw string) []string { // ["13,820", "25"]
			w, f := ban.Amount(ban.Raw(raw), 2)
			return []string{w, f}
		},
		"usd": func(raw string) string {
			p := s.Price()
			if p <= 0 {
				return ""
			}
			v := ban.Float(ban.Raw(raw)) * p
			switch {
			case v >= 100:
				return fmt.Sprintf("$%s", ban.Group(fmt.Sprintf("%.0f", v)))
			case v >= 0.01:
				return fmt.Sprintf("$%.2f", v)
			case v > 0:
				return fmt.Sprintf("$%.4f", v)
			}
			return "$0"
		},
		"name":     s.displayName,
		"ago":      func(ts string) string { return ago(atoi(ts)) },
		"when":     func(ts string) string { return when(atoi(ts)) },
		"agoI":     ago,
		"whenI":    when,
		"addrHTML": addrHTML,
		"monkey": func(addr string) string {
			return "https://monkey.banano.cc/api/v1/monkey/" + addr + "?svc=bananoexplorer"
		},
		"pct": func(part, whole *big.Int) string {
			if whole == nil || whole.Sign() == 0 {
				return "0%"
			}
			f, _ := new(big.Float).Quo(new(big.Float).SetInt(part), new(big.Float).SetInt(whole)).Float64()
			return fmt.Sprintf("%.1f%%", f*100)
		},
		"add":        func(a, b int) int { return a + b },
		"sub":        func(a, b int64) int64 { return a - b },
		"eq2":        func(a, b string) bool { return a == b },
		"isZero":     isZero,
		"asset":      s.asset,
		"sampleFeed": func() feedItem { return feedItem{Hash: strings.Repeat("0", 64), Type: "send", Amount: "0"} },
		"usdPrice":   func(p float64) string { return fmt.Sprintf("$%.6f", p) },
		"usdOf": func(raw *big.Int, p float64) string {
			return "$" + ban.Group(fmt.Sprintf("%.0f", ban.Float(raw)*p))
		},
		"dust":       func(raw string) bool { return ban.Raw(raw).Cmp(dustRaw) < 0 },
		"banNum":     func(raw string) string { return strconv.FormatFloat(ban.Float(ban.Raw(raw)), 'f', -1, 64) },
		"bigBanFrac": func(raw *big.Int) string { return ban.FormatSmart(raw) },
		"day":        func(ts int64) string { return time.Unix(ts, 0).UTC().Format("2 Jan 2006") },
		"pct64": func(a, b int64) int64 {
			if b == 0 {
				return 0
			}
			return a * 100 / b
		},
		"rangeKeys": func() []string { return []string{"1d", "7d", "30d"} },
		"upper":     strings.ToUpper,
		"signed": func(f float64) string {
			sign := "+"
			if f < 0 {
				sign, f = "−", -f
			}
			return sign + ban.Group(fmt.Sprintf("%.0f", f))
		},
		"lower":    strings.ToLower,
		"shortBan": shortBan,
		"bigBan":   func(raw *big.Int) string { return ban.Group(fmt.Sprintf("%.0f", ban.Float(raw))) },
		"repName":  repName,
		"addf":     func(a, b float64) float64 { return a + b },
		"subf":     func(a, b float64) float64 { return a - b },
		"minf":     func(a, b float64) float64 { return min(a, b) },
		"weakest":  weakest,
		"mins": func(m int) string {
			switch {
			case m < 60:
				return fmt.Sprintf("%d min", m)
			case m < 48*60:
				return fmt.Sprintf("%d h", m/60)
			}
			return fmt.Sprintf("%d days", m/1440)
		},
		"gradeClass": func(g string) string {
			if g == "Offline" {
				return "gOff"
			}
			return "g" + g
		},
		"gradeLabel": func(g string) string {
			if g == "Offline" {
				return "OFF"
			}
			return g
		},
		"partColor": func(points, max int) string {
			if points*2 < max {
				return "#C9611E"
			}
			if points*10 < max*9 {
				return "#B08A00"
			}
			return "#338233"
		},
		"pctOf": func(a, b int) int {
			if b == 0 {
				return 0
			}
			return a * 100 / b
		},
		"f1": func(f float64) string { return fmt.Sprintf("%.1f", f) },
		"f2": func(f float64) string { return fmt.Sprintf("%.2f", f) },
		"levelClass": func(l string) string {
			return map[string]string{"good": "in", "note": "amber", "warn": "amber", "bad": "out"}[l]
		},
		"levelText": func(l string) string {
			return map[string]string{"good": "✓ Good", "note": "! Note", "warn": "! Warning", "bad": "✕ Borked"}[l]
		},
		"msWidth":   func(ms int64) int64 { return min(max(ms/5, 4), 80) },
		"sampleRow": func() liveRow { return liveRow{Hash: strings.Repeat("0", 64), Type: "send"} },
		"catLabel":  categoryLabel,
		"typeClass": typeClass,
		"title": func(s string) string {
			if s == "" {
				return s
			}
			return strings.ToUpper(s[:1]) + s[1:]
		},
		"partyArgs": func(label, addr, name string, self bool) map[string]any {
			return map[string]any{"Label": label, "Addr": addr, "Name": name, "Self": self}
		},
	}
}

// addrHTML shows the first 12 and last 6 characters in bold and the middle muted,
// which is how people actually compare addresses.
func addrHTML(a string) template.HTML {
	if len(a) < 20 {
		return template.HTML(template.HTMLEscapeString(a))
	}
	e := template.HTMLEscapeString
	return template.HTML("<b>" + e(a[:12]) + "</b>" + e(a[12:len(a)-6]) + "<b>" + e(a[len(a)-6:]) + "</b>")
}

func typeClass(t string) string {
	switch t {
	case "send":
		return "out"
	case "receive", "open":
		return "in"
	}
	return "chg"
}

// dustRaw is 0.01 BAN; smaller payments hide when the visitor asks.
var dustRaw = new(big.Int).Exp(big.NewInt(10), big.NewInt(27), nil)
