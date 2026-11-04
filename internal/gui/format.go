package gui

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// group inserts thousands separators in the integer part of s.
func group(s string) string {
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	intPart, frac, hasFrac := strings.Cut(s, ".")
	var b strings.Builder
	for i, c := range intPart {
		if i > 0 && (len(intPart)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	out := b.String()
	if hasFrac {
		out += "." + frac
	}
	if neg {
		out = "-" + out
	}
	return out
}

// usd formats a dollar amount with 2 decimals: $1,284.10.
func usd(v float64) string {
	if v < 0 {
		return "-$" + group(strconv.FormatFloat(-v, 'f', 2, 64))
	}
	return "$" + group(strconv.FormatFloat(v, 'f', 2, 64))
}

// usdCents formats minor units (cents) as dollars.
func usdCents(c int64) string { return usd(float64(c) / 100) }

// priceUSD formats a unit price with a precision suited to its magnitude.
func priceUSD(p float64) string {
	a := math.Abs(p)
	switch {
	case a >= 1000:
		return "$" + group(strconv.FormatFloat(p, 'f', 2, 64))
	case a >= 1:
		return "$" + strconv.FormatFloat(p, 'f', 2, 64)
	case a >= 0.01:
		return "$" + strconv.FormatFloat(p, 'f', 4, 64)
	case a == 0:
		return "$0"
	default: // tiny prices: 3 significant digits, no exponent
		decimals := int(-math.Floor(math.Log10(a))) + 2
		s := strings.TrimRight(strconv.FormatFloat(p, 'f', decimals, 64), "0")
		return "$" + s
	}
}

// amount formats a token quantity.
func amount(q float64) string {
	switch a := math.Abs(q); {
	case a >= 1000:
		return group(strconv.FormatFloat(q, 'f', 0, 64))
	case a >= 1:
		return strconv.FormatFloat(q, 'f', 2, 64)
	default:
		return strconv.FormatFloat(q, 'g', 4, 64)
	}
}

func pct(x float64) string { return fmt.Sprintf("%+.2f%%", x*100) }

// arrowPct formats a change with an arrow: ▲ 2.41%.
func arrowPct(x float64) string {
	if x < 0 {
		return fmt.Sprintf("▼ %.2f%%", -x*100)
	}
	return fmt.Sprintf("▲ %.2f%%", x*100)
}

// parseMoney reads an amount in dollars ("10000", "10,000.50" or
// "10 000,50") and returns cents.
func parseMoney(s string) (int64, error) {
	s = strings.TrimPrefix(strings.ReplaceAll(strings.TrimSpace(s), " ", ""), "$")
	switch {
	case strings.Contains(s, "."):
		s = strings.ReplaceAll(s, ",", "") // 10,000.50
	case strings.Count(s, ",") == 1 && len(s)-strings.Index(s, ",")-1 != 3:
		s = strings.Replace(s, ",", ".", 1) // 10000,5 (decimal comma)
	default:
		s = strings.ReplaceAll(s, ",", "") // 10,000 or 1,000,000
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, errors.New("invalid amount")
	}
	if f*100 > math.MaxInt64/2 {
		return 0, errors.New("amount too large")
	}
	return int64(math.Round(f * 100)), nil
}

func shortAddress(a string) string {
	if len(a) <= 12 {
		return a
	}
	return a[:4] + "…" + a[len(a)-4:]
}

func clock(t time.Time) string { return t.Local().Format("15:04:05") }
