// Package money parses, formats and splits amounts held as int64 minor units
// (cents for USD). Floats never touch money (plan §5.1).
package money

import (
	"errors"
	"fmt"
	"strings"

	"golang.org/x/text/currency"
)

// Minus is the true minus sign used for debits (U+2212), never a hyphen.
const Minus = "−"

// MaxAmount bounds any single amount (ten billion major units at exponent 2).
const MaxAmount int64 = 1_000_000_000_000

var (
	ErrEmpty     = errors.New("enter an amount")
	ErrInvalid   = errors.New("that isn't a valid amount")
	ErrPrecision = errors.New("too many decimal places")
	ErrTooLarge  = errors.New("that amount is too large")
)

// Currency describes how to display one ISO 4217 currency.
type Currency struct {
	Code     string
	Exponent int
	Symbol   string
}

var symbols = map[string]string{
	"USD": "$", "CAD": "$", "AUD": "$", "NZD": "$", "SGD": "$", "HKD": "$", "MXN": "$",
	"EUR": "€", "GBP": "£", "JPY": "¥", "CNY": "¥", "INR": "₹", "KRW": "₩",
	"CHF": "CHF ", "SEK": "kr ", "NOK": "kr ", "DKK": "kr ", "PLN": "zł ", "BRL": "R$", "ZAR": "R ",
}

// Lookup returns the Currency for an ISO 4217 code.
func Lookup(code string) (Currency, error) {
	code = strings.ToUpper(strings.TrimSpace(code))
	u, err := currency.ParseISO(code)
	if err != nil {
		return Currency{}, fmt.Errorf("unknown currency %q", code)
	}
	scale, _ := currency.Standard.Rounding(u)
	sym, ok := symbols[code]
	if !ok {
		sym = code + " "
	}
	return Currency{Code: code, Exponent: scale, Symbol: sym}, nil
}

// MustLookup is Lookup for codes known to be valid; it falls back to USD.
func MustLookup(code string) Currency {
	c, err := Lookup(code)
	if err != nil {
		return Currency{Code: "USD", Exponent: 2, Symbol: "$"}
	}
	return c
}

// Parse reads a non-negative amount typed by a person ("5", "5.50",
// "$1,234.5") into minor units. Signs are rejected: the form decides direction.
func (c Currency) Parse(s string) (int64, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, strings.TrimSpace(c.Symbol))
	s = strings.TrimSpace(strings.ReplaceAll(s, ",", ""))
	if s == "" {
		return 0, ErrEmpty
	}
	whole, frac, hasDot := strings.Cut(s, ".")
	if whole == "" && (!hasDot || frac == "") || strings.Contains(frac, ".") {
		return 0, ErrInvalid
	}
	if len(frac) > c.Exponent || (hasDot && c.Exponent == 0) {
		return 0, ErrPrecision
	}
	var v int64
	for _, r := range whole + frac + strings.Repeat("0", c.Exponent-len(frac)) {
		if r < '0' || r > '9' {
			return 0, ErrInvalid
		}
		v = v*10 + int64(r-'0')
		if v > MaxAmount {
			return 0, ErrTooLarge
		}
	}
	return v, nil
}

// Plain formats without a symbol or grouping, e.g. "5.00": for input values.
func (c Currency) Plain(v int64) string {
	sign := ""
	if v < 0 {
		sign, v = "-", -v
	}
	if c.Exponent == 0 {
		return fmt.Sprintf("%s%d", sign, v)
	}
	p := pow10(c.Exponent)
	return fmt.Sprintf("%s%d.%0*d", sign, v/p, c.Exponent, v%p)
}

// Format renders an amount for display: "$1,234.50", or "−$2.50" when negative.
func (c Currency) Format(v int64) string {
	sign := ""
	if v < 0 {
		sign, v = Minus, -v
	}
	return sign + c.Symbol + c.digits(v)
}

// Short is Format without the minor digits when they are zero ("$20" but
// "$2.50"): for chart axes.
func (c Currency) Short(v int64) string {
	if c.Exponent == 0 || v%pow10(c.Exponent) != 0 {
		return c.Format(v)
	}
	sign := ""
	if v < 0 {
		sign, v = Minus, -v
	}
	return sign + c.Symbol + group(v/pow10(c.Exponent))
}

// Signed renders a ledger amount with an explicit sign: "+$5.00" or "−$2.50".
func (c Currency) Signed(v int64) string {
	if v < 0 {
		return c.Format(v)
	}
	return "+" + c.Format(v)
}

func (c Currency) digits(v int64) string {
	p := pow10(c.Exponent)
	whole := group(v / p)
	if c.Exponent == 0 {
		return whole
	}
	return fmt.Sprintf("%s.%0*d", whole, c.Exponent, v%p)
}

func group(n int64) string {
	s := fmt.Sprintf("%d", n)
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

func pow10(n int) int64 {
	p := int64(1)
	for range n {
		p *= 10
	}
	return p
}

// Split divides amount by basis-point weights (which must sum to 10000).
// Each share is floored; the remainder goes to the first part, so the parts
// always sum exactly to amount (plan §5.2).
func Split(amount int64, bps []int) ([]int64, error) {
	if len(bps) == 0 {
		return nil, errors.New("split needs at least one part")
	}
	total := 0
	for _, b := range bps {
		if b < 0 {
			return nil, errors.New("split weights must be non-negative")
		}
		total += b
	}
	if total != 10000 {
		return nil, fmt.Errorf("split weights sum to %d, want 10000", total)
	}
	parts := make([]int64, len(bps))
	var sum int64
	for i, b := range bps {
		parts[i] = amount * int64(b) / 10000
		sum += parts[i]
	}
	parts[0] += amount - sum
	return parts, nil
}
