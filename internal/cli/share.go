package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/ansonnchan/tokenholic/internal/share"
	"github.com/ansonnchan/tokenholic/internal/store"
)

// sharePeriods maps the --period flag to a stored period.
var sharePeriods = map[string]store.Period{
	"today": store.PeriodToday, "week": store.PeriodWeek, "30d": store.PeriodLast30Days, "all": store.PeriodAllTime,
}

// runShareCommand drafts a post about the user's own local totals and prints
// its x.com/intent/post link. It sends nothing: the link opens X's compose
// page in the user's browser, where they review and post it themselves.
//
// When the account's profile is public and the server serves share pages,
// the post also carries the profile's share page, which unfurls with a card.
// Without that the post is text only, and the command says why.
func runShareCommand(args []string, stdout, stderr io.Writer, deps dependencies) int {
	flags := flag.NewFlagSet("tokenholic share", flag.ContinueOnError)
	flags.SetOutput(stderr)
	periodName := flags.String("period", "30d", "period to share: today, week, 30d, or all")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	period, known := sharePeriods[*periodName]
	if flags.NArg() != 0 || !known {
		fmt.Fprintln(stderr, "share accepts only --period today, week, 30d, or all")
		return 2
	}
	home, err := deps.homeDir()
	if err != nil {
		fmt.Fprintln(stderr, "could not resolve the local home directory")
		return 1
	}
	codexHome := deps.getenv("CODEX_HOME")
	if codexHome != "" && !filepath.IsAbs(codexHome) {
		fmt.Fprintln(stderr, "CODEX_HOME must be an absolute directory")
		return 1
	}
	if deps.usage == nil {
		fmt.Fprintln(stderr, "local usage is unavailable")
		return 1
	}
	dashboard, err := deps.usage(context.Background(), home, codexHome)
	if err != nil {
		// Runtime errors can wrap private local paths.
		fmt.Fprintln(stderr, "could not read local usage; run `tokenholic doctor`")
		return 1
	}
	usage, ok := share.UsageFor(dashboard.Summary(period))
	if !ok {
		fmt.Fprintf(stderr, "no usage to share %s; try another --period\n", share.PeriodPhrase(period))
		return 1
	}
	text := share.UsageText(usage)
	card, note := shareCard(deps)
	fmt.Fprintf(stdout, "%s\n\n%s\n\nShare link:\n%s\n", text, note, share.IntentLink(share.Post{Text: text, URL: card}))
	return 0
}

// shareCard returns the share page to attach to the post, or "" with the
// reason there is none. Every failure degrades to a text-only post: a card
// is a nicety, and the draft is the user's own local totals either way.
func shareCard(deps dependencies) (address, note string) {
	apiURL := strings.TrimSpace(deps.getenv("TOKENHOLIC_API_URL"))
	if apiURL == "" || deps.groups == nil {
		return "", "Text only. A card needs an account with a public profile."
	}
	manager, err := deps.groups(apiURL)
	if err != nil {
		return "", "Text only. A card needs a valid TOKENHOLIC_API_URL."
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	link, err := manager.ShareLink(ctx)
	switch {
	case err != nil:
		return "", "Text only. Could not check for a card; sign in with `tokenholic claim` or try again."
	case link.Private:
		return "", "Text only. Your profile is private; `tokenholic profile public` adds a card."
	case link.URL == "":
		return "", "Text only. This server does not serve share pages."
	default:
		return link.URL, "Card: " + link.URL + "\nThe card shows your public profile's last 30 days as the server counts them."
	}
}
