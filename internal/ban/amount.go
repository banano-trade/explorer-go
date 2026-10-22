package ban

import (
	"math/big"
	"strings"
)

// Raw parses a raw amount string; invalid input yields zero.
func Raw(s string) *big.Int {
	n, ok := new(big.Int).SetString(strings.TrimSpace(s), 10)
	if !ok {
		return new(big.Int)
	}
	return n
}

// Amount splits a raw value into whole BAN (with thousands separators) and up to
// maxDec decimals with trailing zeros trimmed, e.g. "13,820" and "25".
func Amount(raw *big.Int, maxDec int) (whole, frac string) {
	neg := raw.Sign() < 0
	a := new(big.Int).Abs(raw)
	q, r := new(big.Int).QuoRem(a, RawPerBAN, new(big.Int))
	whole = group(q.String())
	if neg {
		whole = "-" + whole
	}
	if maxDec > 0 && r.Sign() > 0 {
		f := r.String()
		f = strings.Repeat("0", 29-len(f)) + f
		if len(f) > maxDec {
			f = f[:maxDec]
		}
		frac = strings.TrimRight(f, "0")
	}
	return whole, frac
}

// Format renders a raw value as "13,820.25" with up to maxDec decimals.
func Format(raw *big.Int, maxDec int) string {
	w, f := Amount(raw, maxDec)
	if f == "" {
		return w
	}
	return w + "." + f
}

// FormatSmart picks decimals so small amounts stay readable: 2 decimals for
// amounts of 1 BAN or more, up to 6 below that.
func FormatSmart(raw *big.Int) string {
	if new(big.Int).Abs(raw).Cmp(RawPerBAN) >= 0 {
		return Format(raw, 2)
	}
	s := Format(raw, 6)
	if s == "0" && raw.Sign() != 0 {
		return "<0.000001"
	}
	return s
}

// Float converts raw to a float64 BAN value (for ratios and fiat only, never for display of exact amounts).
func Float(raw *big.Int) float64 {
	f, _ := new(big.Float).Quo(new(big.Float).SetInt(raw), new(big.Float).SetInt(RawPerBAN)).Float64()
	return f
}

func group(s string) string {
	if len(s) <= 3 {
		return s
	}
	var b strings.Builder
	pre := len(s) % 3
	if pre > 0 {
		b.WriteString(s[:pre])
	}
	for i := pre; i < len(s); i += 3 {
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		b.WriteString(s[i : i+3])
	}
	return b.String()
}

// Group adds thousands separators to a plain integer string.
func Group(s string) string { return group(s) }
