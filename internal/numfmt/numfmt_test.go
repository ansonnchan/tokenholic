package numfmt

import (
	"encoding/json"
	"os"
	"testing"
)

func TestTokensKeepsThreeSignificantDigitsAndPromotesUnits(t *testing.T) {
	t.Parallel()
	cases := []struct {
		count uint64
		want  string
	}{
		{0, "0"},
		{7, "7"},
		{987, "987"},
		{999, "999"},
		{1_000, "1K"},
		{1_004, "1K"},
		{1_005, "1.01K"},
		{1_234, "1.23K"},
		{12_345, "12.3K"},
		{111_000, "111K"},
		{123_456, "123K"},
		{999_499, "999K"},
		{999_500, "1M"},
		{999_950, "1M"},
		{1_000_000, "1M"},
		{1_350_000, "1.35M"},
		{2_100_000_000, "2.1B"},
		{999_999_999, "1B"},
		{4_270_000_000_000, "4.27T"},
		{999_999_999_999, "1T"},
		{999_600_000_000_000, "1,000T"},
		{1_234_000_000_000_000, "1,234T"},
		{18_446_744_073_709_551_615, "18,446,744T"},
	}
	for _, test := range cases {
		if got := Tokens(test.count); got != test.want {
			t.Errorf("Tokens(%d) = %q, want %q", test.count, got, test.want)
		}
	}
}

func TestDecimalFormsRejectAnythingButDigits(t *testing.T) {
	t.Parallel()
	if got, ok := TokensDecimal("340282366920938463463374607431768211456"); !ok || got != "340,282,366,920,938,463,463,374,607T" {
		t.Errorf("TokensDecimal(2^128) = %q, %t", got, ok)
	}
	if got, ok := ExactDecimal("1234567"); !ok || got != "1,234,567" {
		t.Errorf("ExactDecimal = %q, %t", got, ok)
	}
	if got, ok := USDDecimal("1180000000"); !ok || got != "$1,180.00" {
		t.Errorf("USDDecimal = %q, %t", got, ok)
	}
	for _, invalid := range []string{"", "-1", "+1", "1.5", "1e3", " 1", "abc"} {
		if _, ok := TokensDecimal(invalid); ok {
			t.Errorf("TokensDecimal(%q) accepted", invalid)
		}
		if _, ok := ExactDecimal(invalid); ok {
			t.Errorf("ExactDecimal(%q) accepted", invalid)
		}
		if _, ok := USDDecimal(invalid); ok {
			t.Errorf("USDDecimal(%q) accepted", invalid)
		}
	}
}

func TestExactGroupsThousands(t *testing.T) {
	t.Parallel()
	cases := map[uint64]string{0: "0", 999: "999", 1_000: "1,000", 12_345: "12,345", 1_234_567: "1,234,567"}
	for count, want := range cases {
		if got := Exact(count); got != want {
			t.Errorf("Exact(%d) = %q, want %q", count, got, want)
		}
	}
}

func TestUSDNeverShowsANonZeroAmountAsFree(t *testing.T) {
	t.Parallel()
	cases := []struct {
		micros uint64
		want   string
	}{
		{0, "$0.00"},
		{1, "<$0.01"},
		{9_999, "<$0.01"},
		{10_000, "$0.01"},
		{14_999, "$0.01"},
		{15_000, "$0.02"},
		{995_000, "$1.00"},
		{640_000_000, "$640.00"},
		{840_500_000, "$840.50"},
		{1_180_000_000, "$1,180.00"},
		{1_234_567_894_999, "$1,234,567.89"},
		{1_234_567_895_000, "$1,234,567.90"},
	}
	for _, test := range cases {
		if got := USD(test.micros); got != test.want {
			t.Errorf("USD(%d) = %q, want %q", test.micros, got, test.want)
		}
	}
}

func TestPercentCutsInsteadOfRounding(t *testing.T) {
	t.Parallel()
	cases := map[int]string{
		-5: "0%", 0: "0%", 1: "<0.1%", 9: "<0.1%", 10: "0.1%", 7100: "71%", 9200: "92%",
		9405: "94%", 9449: "94.4%", 9499: "94.9%", 9500: "95%", 9999: "99.9%", 10000: "100%",
	}
	for basisPoints, want := range cases {
		if got := Percent(basisPoints); got != want {
			t.Errorf("Percent(%d) = %q, want %q", basisPoints, got, want)
		}
	}
}

// The website formats the same values in JavaScript. Both read these
// vectors, so a change to one formatter fails the other's tests.
func TestSharedVectors(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile("testdata/vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var vectors struct {
		Tokens  []struct{ In, Want string } `json:"tokens"`
		USD     []struct{ In, Want string } `json:"usd_micros"`
		Percent []struct {
			In   int
			Want string
		} `json:"percent_basis_points"`
		Invalid []string `json:"invalid_decimals"`
	}
	if err := json.Unmarshal(raw, &vectors); err != nil {
		t.Fatal(err)
	}
	if len(vectors.Tokens) == 0 || len(vectors.USD) == 0 || len(vectors.Percent) == 0 || len(vectors.Invalid) == 0 {
		t.Fatal("vectors file is missing a section")
	}
	for _, vector := range vectors.Tokens {
		if got, ok := TokensDecimal(vector.In); !ok || got != vector.Want {
			t.Errorf("TokensDecimal(%q) = %q, %t; want %q", vector.In, got, ok, vector.Want)
		}
	}
	for _, vector := range vectors.USD {
		if got, ok := USDDecimal(vector.In); !ok || got != vector.Want {
			t.Errorf("USDDecimal(%q) = %q, %t; want %q", vector.In, got, ok, vector.Want)
		}
	}
	for _, vector := range vectors.Percent {
		if got := Percent(vector.In); got != vector.Want {
			t.Errorf("Percent(%d) = %q, want %q", vector.In, got, vector.Want)
		}
	}
	for _, invalid := range vectors.Invalid {
		if _, ok := TokensDecimal(invalid); ok {
			t.Errorf("TokensDecimal(%q) accepted", invalid)
		}
	}
}
