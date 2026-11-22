// Package share builds links that let a user post their own totals. A link
// is opened by the user's browser, which is where they review and send the
// post: nothing here calls a social network's API or uploads anything.
package share

import (
	"net/url"
	"strings"

	"github.com/ansonnchan/tokenholic/internal/numfmt"
	"github.com/ansonnchan/tokenholic/internal/store"
)

// intentEndpoint is X's public compose page. It takes the draft in the query
// string and needs no API key.
const intentEndpoint = "https://x.com/intent/post"

// Post is a draft the user can review before sending.
type Post struct {
	// Text is the draft. It should hold only what the user chose to share.
	Text string
	// URL, when set, is attached as the post's link.
	URL string
}

// IntentLink returns the compose link for a post.
func IntentLink(post Post) string {
	query := url.Values{"text": []string{strings.TrimSpace(post.Text)}}
	if link := strings.TrimSpace(post.URL); link != "" {
		query.Set("url", link)
	}
	return intentEndpoint + "?" + query.Encode()
}

// Usage describes totals for a draft, already formatted for display. A
// field left empty is left out of the text.
type Usage struct {
	// Tokens is a token count such as "4.26B".
	Tokens string
	// Estimate is an API-equivalent estimate such as "$1,180.42".
	Estimate string
	// LowerBound says the totals are at least what is stated.
	LowerBound bool
	// Period is a phrase that ends the first sentence, such as
	// "in the last 30 days" or "this week".
	Period string
}

// UsageText drafts a post about the user's own usage. The estimate is
// always called an estimate, never spending. It names no group: a private
// group's name and ranks do not belong in a public post.
func UsageText(usage Usage) string {
	if usage.Tokens == "" {
		return ""
	}
	bound := ""
	if usage.LowerBound {
		bound = "at least "
	}
	var text strings.Builder
	text.WriteString("I burned " + bound + usage.Tokens + " tokens with coding agents")
	if usage.Period != "" {
		text.WriteString(" " + usage.Period)
	}
	text.WriteString(".")
	if usage.Estimate != "" {
		text.WriteString(" That is " + bound + usage.Estimate + " at API list prices (an estimate).")
	}
	text.WriteString("\n\n>_ npx @tokenholic/cli")
	return text.String()
}

// UsageFor describes one period's local totals for a draft. It reports false
// when the period has no token total to state, because there is nothing to
// post: an unknown total is never drafted as zero, and neither is a lower
// bound of zero, which only means nothing could be counted. Either value
// being a lower bound makes the whole draft say "at least".
func UsageFor(summary store.UsageSummary) (Usage, bool) {
	total := summary.Tokens.Total()
	if total.Value == nil || *total.Value == 0 {
		return Usage{}, false
	}
	usage := Usage{
		Tokens:     numfmt.Tokens(*total.Value),
		LowerBound: total.State == store.AvailabilityPartial || summary.APIEstimate.State == store.AvailabilityPartial,
		Period:     PeriodPhrase(summary.Period),
	}
	if summary.APIEstimate.Value != nil {
		usage.Estimate = numfmt.USD(*summary.APIEstimate.Value)
	}
	return usage, true
}

// PeriodPhrase is how a draft names a period at the end of its sentence.
func PeriodPhrase(period store.Period) string {
	switch period {
	case store.PeriodToday:
		return "today"
	case store.PeriodWeek:
		return "this week"
	case store.PeriodLast30Days:
		return "in the last 30 days"
	default:
		return "so far"
	}
}
