// Package numfmt formats token counts, API-equivalent estimates, and shares
// the way every tokenholic surface displays them. It formats known values
// only: callers decide what an unknown or unavailable value looks like, so a
// missing number can never be rendered as zero here.
package numfmt

import (
	"math/big"
	"strconv"
	"strings"
)

// microsPerCent is one cent in micro-USD, the unit estimates are stored in.
const microsPerCent = 10_000

var (
	thousand   = big.NewInt(1000)
	ten        = big.NewInt(10)
	tokenUnits = []string{"", "K", "M", "B", "T"}
)

// Tokens renders a count compactly with at most three significant digits and
// no trailing zeros: 987, 1K, 111K, 1.35M, 2.1B, 4.27T. Rounding is half up
// and never yields 1000 of a unit; 999,950 is 1M.
func Tokens(count uint64) string {
	return compact(new(big.Int).SetUint64(count))
}

// TokensDecimal is Tokens for a non-negative decimal string, the form the
// API uses for sums that may exceed 64 bits. It reports false for anything
// else.
func TokensDecimal(count string) (string, bool) {
	value, ok := parseDecimal(count)
	if !ok {
		return "", false
	}
	return compact(value), true
}

// Exact renders a count in full with thousands separators, for detail views.
func Exact(count uint64) string {
	return group(strconv.FormatUint(count, 10))
}

// ExactDecimal is Exact for a non-negative decimal string.
func ExactDecimal(count string) (string, bool) {
	value, ok := parseDecimal(count)
	if !ok {
		return "", false
	}
	return group(value.String()), true
}

// USD renders micro-USD as dollars and cents with grouping: $1,180.00.
// A non-zero amount below one cent is "<$0.01" so it never reads as free.
// The result carries no "EST" label; the caller adds it.
func USD(micros uint64) string {
	return usd(new(big.Int).SetUint64(micros))
}

// USDDecimal is USD for a non-negative decimal string of micro-USD.
func USDDecimal(micros string) (string, bool) {
	value, ok := parseDecimal(micros)
	if !ok {
		return "", false
	}
	return usd(value), true
}

// Percent renders basis points as a percentage. Fractions are cut, not
// rounded, to one decimal, so a share just under a threshold never displays
// as having reached it: 9499 is "94.9%".
func Percent(basisPoints int) string {
	if basisPoints < 0 {
		basisPoints = 0
	}
	whole, tenths := basisPoints/100, basisPoints%100/10
	switch {
	case tenths > 0:
		return strconv.Itoa(whole) + "." + strconv.Itoa(tenths) + "%"
	case whole == 0 && basisPoints > 0:
		return "<0.1%"
	default:
		return strconv.Itoa(whole) + "%"
	}
}

func parseDecimal(value string) (*big.Int, bool) {
	if value == "" {
		return nil, false
	}
	for _, digit := range value {
		if digit < '0' || digit > '9' {
			return nil, false
		}
	}
	parsed, ok := new(big.Int).SetString(value, 10)
	return parsed, ok
}

func compact(value *big.Int) string {
	digits := value.String()
	if len(digits) <= 3 {
		return digits
	}
	unit := min((len(digits)-1)/3, len(tokenUnits)-1)
	divisor := new(big.Int).Exp(thousand, big.NewInt(int64(unit)), nil)
	// Keep three significant digits: the fewer whole digits, the more decimals.
	decimals := max(0, 3-(len(digits)-3*unit))
	scale := new(big.Int).Exp(ten, big.NewInt(int64(decimals)), nil)
	scaled := new(big.Int).Mul(value, scale)
	scaled.Add(scaled, new(big.Int).Rsh(divisor, 1)) // round half up
	scaled.Quo(scaled, divisor)
	if unit < len(tokenUnits)-1 && scaled.Cmp(new(big.Int).Mul(thousand, scale)) >= 0 {
		// Rounding reached 1000 of this unit, which is 1 of the next.
		return "1" + tokenUnits[unit+1]
	}
	text := scaled.String()
	if decimals == 0 {
		return group(text) + tokenUnits[unit]
	}
	integer, fraction := text[:len(text)-decimals], strings.TrimRight(text[len(text)-decimals:], "0")
	if fraction == "" {
		return integer + tokenUnits[unit]
	}
	return integer + "." + fraction + tokenUnits[unit]
}

func usd(micros *big.Int) string {
	if micros.Sign() > 0 && micros.Cmp(big.NewInt(microsPerCent)) < 0 {
		return "<$0.01"
	}
	cents := new(big.Int).Add(micros, big.NewInt(microsPerCent/2))
	cents.Quo(cents, big.NewInt(microsPerCent))
	dollars, remainder := new(big.Int).QuoRem(cents, big.NewInt(100), new(big.Int))
	fraction := remainder.String()
	if len(fraction) < 2 {
		fraction = "0" + fraction
	}
	return "$" + group(dollars.String()) + "." + fraction
}

func group(digits string) string {
	if len(digits) <= 3 {
		return digits
	}
	var grouped strings.Builder
	head := len(digits) % 3
	if head > 0 {
		grouped.WriteString(digits[:head])
	}
	for index := head; index < len(digits); index += 3 {
		if grouped.Len() > 0 {
			grouped.WriteByte(',')
		}
		grouped.WriteString(digits[index : index+3])
	}
	return grouped.String()
}
