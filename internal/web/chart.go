package web

import (
	"fmt"
	"html/template"
	"math"
	"strings"
	"time"
)

// Line is one series for lineChart. Color is a CSS value (use the --c1/--c2 tokens).
type Line struct {
	Name  string
	Color string
	Dash  bool
	Step  bool    // hold each value until the next point (balances)
	X     []int64 // unix seconds
	Y     []float64
}

const (
	chartW, chartH = 1000, 220
	padL, padR     = 0, 8 // the y labels sit in a fixed 52px gutter outside the SVG
	padT, padB     = 12, 28
)

// lineChart draws series as an SVG with gridlines, axis labels and a hover
// tooltip per time slot (native <title>, so it needs no script). The y axis
// starts at zero so changes aren't exaggerated.
func lineChart(label, unit string, lines []Line) template.HTML {
	var minX, maxX int64 = math.MaxInt64, math.MinInt64
	maxY := 0.0
	n := 0
	for _, l := range lines {
		for i, x := range l.X {
			minX, maxX = min(minX, x), max(maxX, x)
			maxY = math.Max(maxY, l.Y[i])
			n++
		}
	}
	if n < 2 || maxX == minX {
		return template.HTML(`<div class="chart-empty">Not enough data yet. The explorer samples the node every minute and draws this as it goes.</div>`)
	}
	top := niceCeil(maxY)
	px := func(x int64) float64 {
		return padL + float64(x-minX)/float64(maxX-minX)*float64(chartW-padL-padR)
	}
	py := func(y float64) float64 { return padT + (1-y/top)*float64(chartH-padT-padB) }

	var b, labels strings.Builder
	b.WriteString(`<div class="chartbox">`)
	fmt.Fprintf(&b, `<svg class="chart" viewBox="0 0 %d %d" preserveAspectRatio="none" role="img" aria-label="%s">`, chartW, chartH, template.HTMLEscapeString(label))
	for i := 0; i <= 4; i++ {
		v := top * float64(i) / 4
		y := py(v)
		fmt.Fprintf(&b, `<line class="grid" x1="%d" x2="%d" y1="%.1f" y2="%.1f"/>`, padL, chartW-padR, y, y)
		fmt.Fprintf(&labels, `<span class="ay" style="top: %.2f%%">%s</span>`, y/chartH*100, axisNum(v))
	}
	span := time.Duration(maxX-minX) * time.Second
	for i := 0; i <= 3; i++ {
		x := minX + (maxX-minX)*int64(i)/3
		shift := "-50%"
		if i == 0 {
			shift = "0"
		} else if i == 3 {
			shift = "-100%"
		}
		fmt.Fprintf(&labels, `<span class="axx" style="left: calc(52px + (100%% - 52px) * %.4f); transform: translateX(%s)">%s</span>`, px(x)/chartW, shift, axisTime(x, span))
	}
	for _, l := range lines {
		var pts []string
		for i, x := range l.X {
			if l.Step && i > 0 {
				pts = append(pts, fmt.Sprintf("%.1f,%.1f", px(x), py(l.Y[i-1])))
			}
			pts = append(pts, fmt.Sprintf("%.1f,%.1f", px(x), py(l.Y[i])))
		}
		dash := ""
		if l.Dash {
			dash = ` stroke-dasharray="6 4"`
		}
		fmt.Fprintf(&b, `<polyline fill="none" stroke="%s" stroke-width="2" stroke-linejoin="round" stroke-linecap="round" vector-effect="non-scaling-stroke"%s points="%s"/>`, l.Color, dash, strings.Join(pts, " "))
	}
	// Hover slots: one per point of the first series, covering the full height.
	if len(lines) > 0 {
		xs := lines[0].X
		for i, x := range xs {
			left := padL - 0.0
			if i > 0 {
				left = (px(xs[i-1]) + px(x)) / 2
			}
			right := float64(chartW - padR)
			if i < len(xs)-1 {
				right = (px(x) + px(xs[i+1])) / 2
			}
			var tip []string
			tip = append(tip, axisTimeFull(x, span))
			for _, l := range lines {
				if i < len(l.Y) {
					tip = append(tip, fmt.Sprintf("%s: %s%s", l.Name, axisNum(l.Y[i]), unit))
				}
			}
			fmt.Fprintf(&b, `<rect class="hit" x="%.1f" y="0" width="%.1f" height="%d"><title>%s</title></rect>`, left, math.Max(right-left, 1), chartH-padB, template.HTMLEscapeString(strings.Join(tip, "\n")))
		}
	}
	b.WriteString(`</svg>`)
	b.WriteString(labels.String())
	b.WriteString(`</div>`)
	if len(lines) > 1 {
		b.WriteString(`<div class="keys">`)
		for _, l := range lines {
			cls := ""
			if l.Dash {
				cls = " dash"
			}
			fmt.Fprintf(&b, `<span class="key"><i class="line%s" style="border-color: %s"></i>%s</span>`, cls, l.Color, template.HTMLEscapeString(l.Name))
		}
		b.WriteString(`</div>`)
	}
	return template.HTML(b.String())
}

func niceCeil(v float64) float64 {
	if v <= 0 {
		return 1
	}
	p := math.Pow(10, math.Floor(math.Log10(v)))
	for _, m := range []float64{1, 2, 2.5, 5, 10} {
		if m*p >= v {
			return m * p
		}
	}
	return 10 * p
}

func axisNum(v float64) string {
	switch {
	case v >= 1e9:
		return trimZero(fmt.Sprintf("%.2f", v/1e9)) + "B"
	case v >= 1e6:
		return trimZero(fmt.Sprintf("%.1f", v/1e6)) + "M"
	case v >= 1e4:
		return trimZero(fmt.Sprintf("%.1f", v/1e3)) + "K"
	case v >= 100 || v == math.Trunc(v):
		return fmt.Sprintf("%.0f", v)
	case v >= 1:
		return trimZero(fmt.Sprintf("%.1f", v))
	}
	return trimZero(fmt.Sprintf("%.2f", v))
}

func trimZero(s string) string {
	if strings.Contains(s, ".") {
		s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	}
	return s
}

func axisTime(ts int64, span time.Duration) string {
	t := time.Unix(ts, 0).UTC()
	switch {
	case span <= 36*time.Hour:
		return t.Format("15:04")
	case span > 300*24*time.Hour:
		return t.Format("Jan 2006")
	}
	return t.Format("2 Jan")
}

func axisTimeFull(ts int64, span time.Duration) string {
	t := time.Unix(ts, 0).UTC()
	switch {
	case span <= 36*time.Hour:
		return t.Format("15:04 UTC")
	case span > 60*24*time.Hour:
		return t.Format("2 Jan 2006")
	}
	return t.Format("2 Jan, 15:00 UTC")
}
