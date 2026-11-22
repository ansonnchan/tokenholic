package share

import (
	"net/url"
	"strings"
	"testing"

	"github.com/ansonnchan/tokenholic/internal/store"
)

func TestIntentLinkEncodesTheDraftForXsComposePage(t *testing.T) {
	t.Parallel()
	link := IntentLink(Post{Text: "  I burned 4.26B tokens & more.\n\n>_ npx @tokenholic/cli  ", URL: " https://tokenholic.example/u/synthetic "})
	parsed, err := url.Parse(link)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Scheme != "https" || parsed.Host != "x.com" || parsed.Path != "/intent/post" {
		t.Fatalf("link = %q", link)
	}
	if got := parsed.Query().Get("text"); got != "I burned 4.26B tokens & more.\n\n>_ npx @tokenholic/cli" {
		t.Errorf("text = %q", got)
	}
	if got := parsed.Query().Get("url"); got != "https://tokenholic.example/u/synthetic" {
		t.Errorf("url = %q", got)
	}
	// Nothing in the draft may break out of the query string.
	if strings.ContainsAny(strings.TrimPrefix(link, "https://x.com/intent/post?"), " \n&#") && strings.Count(link, "&") != 1 {
		t.Errorf("link is not safely encoded: %q", link)
	}
	if got := IntentLink(Post{Text: "text only"}); got != "https://x.com/intent/post?text=text+only" {
		t.Errorf("link without a URL = %q", got)
	}
}

func TestUsageTextLabelsEstimatesAndLowerBounds(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		usage Usage
		want  string
	}{
		{"complete", Usage{Tokens: "4.26B", Estimate: "$1,180.42", Period: "in the last 30 days"},
			"I burned 4.26B tokens with coding agents in the last 30 days. That is $1,180.42 at API list prices (an estimate).\n\n>_ npx @tokenholic/cli"},
		{"lower bound", Usage{Tokens: "4.26B", Estimate: "$1,180.42", LowerBound: true, Period: "in the last 30 days"},
			"I burned at least 4.26B tokens with coding agents in the last 30 days. That is at least $1,180.42 at API list prices (an estimate).\n\n>_ npx @tokenholic/cli"},
		{"no estimate", Usage{Tokens: "987", Period: "today"},
			"I burned 987 tokens with coding agents today.\n\n>_ npx @tokenholic/cli"},
		{"no period", Usage{Tokens: "1M", Estimate: "$3.20"},
			"I burned 1M tokens with coding agents. That is $3.20 at API list prices (an estimate).\n\n>_ npx @tokenholic/cli"},
		{"nothing to share", Usage{Estimate: "$3.20"}, ""},
	}
	for _, test := range cases {
		got := UsageText(test.usage)
		if got != test.want {
			t.Errorf("%s: UsageText() = %q, want %q", test.name, got, test.want)
		}
		if lower := strings.ToLower(got); strings.Contains(lower, "spent") || strings.Contains(lower, "spend") || strings.Contains(lower, "paid") {
			t.Errorf("%s: an estimate is described as spending: %q", test.name, got)
		}
		if strings.Contains(got, "$") && !strings.Contains(got, "estimate") {
			t.Errorf("%s: a dollar amount is not called an estimate: %q", test.name, got)
		}
	}
}

func TestUsageForNeverDraftsAnUnknownTotal(t *testing.T) {
	t.Parallel()
	count := func(value uint64, state store.Availability) store.AggregateValue {
		return store.AggregateValue{Value: &value, State: state}
	}
	tokens := func(output store.AggregateValue) store.TokenBreakdown {
		zero := count(0, store.AvailabilityComplete)
		return store.TokenBreakdown{InputUncached: zero, InputCacheRead: zero, InputCacheWrite: zero, Output: output}
	}
	known := store.UsageSummary{
		Period: store.PeriodLast30Days, Tokens: tokens(count(4_263_000_000, store.AvailabilityComplete)),
		APIEstimate: count(1_180_420_000, store.AvailabilityComplete),
	}
	usage, ok := UsageFor(known)
	if !ok || usage != (Usage{Tokens: "4.26B", Estimate: "$1,180.42", Period: "in the last 30 days"}) {
		t.Fatalf("UsageFor(known) = %+v, %t", usage, ok)
	}

	partial := known
	partial.Period, partial.APIEstimate = store.PeriodWeek, count(1_180_420_000, store.AvailabilityPartial)
	if usage, ok := UsageFor(partial); !ok || !usage.LowerBound || usage.Period != "this week" {
		t.Fatalf("UsageFor(partial estimate) = %+v, %t", usage, ok)
	}

	unpriced := known
	unpriced.Period, unpriced.APIEstimate = store.PeriodAllTime, store.AggregateValue{State: store.AvailabilityUnavailable}
	usage, ok = UsageFor(unpriced)
	if !ok || usage.Estimate != "" || usage.Period != "so far" || strings.Contains(UsageText(usage), "$") {
		t.Fatalf("UsageFor(unpriced) = %+v, %t: %q", usage, ok, UsageText(usage))
	}

	for name, summary := range map[string]store.UsageSummary{
		"no usage":      {Period: store.PeriodToday},
		"unknown total": {Period: store.PeriodToday, Tokens: tokens(store.AggregateValue{State: store.AvailabilityUnavailable})},
	} {
		if usage, ok := UsageFor(summary); ok || usage != (Usage{}) {
			t.Errorf("UsageFor(%s) = %+v, %t; want nothing to post", name, usage, ok)
		}
	}
	if PeriodPhrase(store.PeriodToday) != "today" {
		t.Errorf("PeriodPhrase(today) = %q", PeriodPhrase(store.PeriodToday))
	}
}
