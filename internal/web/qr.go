package web

import (
	"net/http"
	"regexp"

	"fmt"
	"github.com/banano-trade/explorer-go/internal/ban"
	"html/template"
	"strings"

	"rsc.io/qr"
)

// qrSVG renders text as a QR code: an SVG of one path, crisp at any size.
func qrSVG(text string) template.HTML {
	c, err := qr.Encode(text, qr.M)
	if err != nil {
		return ""
	}
	const quiet = 2
	n := c.Size + 2*quiet
	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" class="qrsvg" role="img" aria-label="QR code" shape-rendering="crispEdges"><rect width="%d" height="%d" fill="#fff"/><path fill="#212124" d="`, n, n, n, n)
	for y := 0; y < c.Size; y++ {
		for x := 0; x < c.Size; x++ {
			if c.Black(x, y) {
				fmt.Fprintf(&b, "M%d %dh1v1h-1z", x+quiet, y+quiet)
			}
		}
	}
	b.WriteString(`"/></svg>`)
	return template.HTML(b.String())
}

var rawAmount = regexp.MustCompile(`^[1-9][0-9]{0,38}$`)

// qrImage serves /qr/<address>.svg[?amount=raw] as a payment QR code.
func (s *Server) qrImage(w http.ResponseWriter, r *http.Request) {
	f := r.PathValue("file")
	addr := strings.TrimSuffix(f, ".svg")
	if addr == f {
		http.NotFound(w, r)
		return
	}
	if _, err := ban.ParseAddress(addr); err != nil {
		http.NotFound(w, r)
		return
	}
	uri := "ban:" + addr
	if a := r.URL.Query().Get("amount"); a != "" {
		if !rawAmount.MatchString(a) {
			http.Error(w, "bad amount", http.StatusBadRequest)
			return
		}
		uri += "?amount=" + a
	}
	w.Header().Set("Content-Type", "image/svg+xml")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'")
	_, _ = w.Write([]byte(qrSVG(uri)))
}
