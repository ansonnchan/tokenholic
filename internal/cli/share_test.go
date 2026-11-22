package cli

import (
	"bytes"
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/ansonnchan/tokenholic/internal/application"
	"github.com/ansonnchan/tokenholic/internal/multiplayer"
	"github.com/ansonnchan/tokenholic/internal/store"
)

func shareSummary(period store.Period, tokens, estimate uint64, state store.Availability) store.UsageSummary {
	zero := uint64(0)
	known := store.AggregateValue{Value: &zero, State: store.AvailabilityComplete}
	return store.UsageSummary{
		Period:      period,
		Tokens:      store.TokenBreakdown{InputUncached: known, InputCacheRead: known, InputCacheWrite: known, Output: store.AggregateValue{Value: &tokens, State: state}},
		APIEstimate: store.AggregateValue{Value: &estimate, State: state},
	}
}

func shareDependencies(manager *fakeGroupManager, apiURL string) dependencies {
	return dependencies{
		homeDir: func() (string, error) { return "/synthetic/home", nil },
		getenv: func(key string) string {
			if key == "TOKENHOLIC_API_URL" {
				return apiURL
			}
			return ""
		},
		groups: func(string) (groupManager, error) { return manager, nil },
		usage: func(context.Context, string, string) (application.Dashboard, error) {
			return application.Dashboard{
				Today:      store.UsageSummary{Period: store.PeriodToday},
				Week:       shareSummary(store.PeriodWeek, 111_000_000, 31_500_000, store.AvailabilityComplete),
				Last30Days: shareSummary(store.PeriodLast30Days, 4_263_000_000, 1_180_420_000, store.AvailabilityPartial),
				AllTime:    shareSummary(store.PeriodAllTime, 21_870_000_000, 6_204_180_000, store.AvailabilityPartial),
			}, nil
		},
	}
}

func TestShareCommandPrintsADraftAndAnIntentLink(t *testing.T) {
	t.Parallel()
	public := multiplayer.ShareLink{Login: "member", URL: "https://api.example.test/s/u/member"}
	tests := []struct {
		name     string
		args     []string
		apiURL   string
		manager  *fakeGroupManager
		wantText string
		wantNote string
		wantCard string
	}{
		{
			name: "public profile attaches its share page", args: []string{"share"}, apiURL: "https://api.example.test",
			manager:  &fakeGroupManager{shareLink: public},
			wantText: "I burned at least 4.26B tokens with coding agents in the last 30 days. That is at least $1,180.42 at API list prices (an estimate).",
			wantNote: "Card: https://api.example.test/s/u/member", wantCard: "https://api.example.test/s/u/member",
		},
		{
			name: "a chosen period", args: []string{"share", "--period", "week"}, apiURL: "https://api.example.test",
			manager:  &fakeGroupManager{shareLink: public},
			wantText: "I burned 111M tokens with coding agents this week. That is $31.50 at API list prices (an estimate).",
			wantNote: "Card: ", wantCard: "https://api.example.test/s/u/member",
		},
		{
			name: "private profile is text only", args: []string{"share", "--period=all"}, apiURL: "https://api.example.test",
			manager:  &fakeGroupManager{shareLink: multiplayer.ShareLink{Login: "member", Private: true}},
			wantText: "I burned at least 21.9B tokens with coding agents so far.",
			wantNote: "Text only. Your profile is private; `tokenholic profile public` adds a card.",
		},
		{
			name: "server without share pages", args: []string{"share"}, apiURL: "https://api.example.test",
			manager:  &fakeGroupManager{shareLink: multiplayer.ShareLink{Login: "member"}},
			wantText: "I burned at least 4.26B tokens", wantNote: "Text only. This server does not serve share pages.",
		},
		{
			name: "signed out or unreachable", args: []string{"share"}, apiURL: "https://api.example.test",
			manager:  &fakeGroupManager{shareErr: errors.New("open /private/path/session: denied")},
			wantText: "I burned at least 4.26B tokens", wantNote: "Text only. Could not check for a card",
		},
		{
			name: "no API configured", args: []string{"share"},
			manager:  &fakeGroupManager{shareLink: public},
			wantText: "I burned at least 4.26B tokens", wantNote: "Text only. A card needs an account with a public profile.",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var stdout, stderr bytes.Buffer
			if code := run(test.args, &stdout, &stderr, shareDependencies(test.manager, test.apiURL)); code != 0 || stderr.Len() != 0 {
				t.Fatalf("run(%v) = %d, stderr %q", test.args, code, stderr.String())
			}
			output := stdout.String()
			if !strings.Contains(output, test.wantText) || !strings.Contains(output, test.wantNote) || strings.Contains(output, "/private/path") {
				t.Fatalf("output = %q", output)
			}
			// The link is the last line, alone, so it can be selected.
			lines := strings.Split(strings.TrimRight(output, "\n"), "\n")
			if lines[len(lines)-2] != "Share link:" {
				t.Fatalf("output does not end with the link: %q", output)
			}
			link, err := url.Parse(lines[len(lines)-1])
			if err != nil || link.Scheme != "https" || link.Host != "x.com" || link.Path != "/intent/post" {
				t.Fatalf("link = %q, %v", lines[len(lines)-1], err)
			}
			if text := link.Query().Get("text"); !strings.Contains(text, test.wantText) || !strings.HasSuffix(text, ">_ npx @tokenholic/cli") {
				t.Errorf("link text = %q", text)
			}
			if got := link.Query().Get("url"); got != test.wantCard {
				t.Errorf("link url = %q, want %q", got, test.wantCard)
			}
			if strings.Contains(strings.ToLower(output), "spent") {
				t.Errorf("an estimate is described as spent: %q", output)
			}
		})
	}
}

func TestShareCommandRefusesWhatItCannotDraft(t *testing.T) {
	t.Parallel()
	manager := &fakeGroupManager{}
	tests := []struct {
		name     string
		args     []string
		mutate   func(*dependencies)
		wantCode int
		wantErr  string
	}{
		{name: "a period with no usage", args: []string{"share", "--period", "today"}, wantCode: 1, wantErr: "no usage to share today"},
		{name: "unknown period", args: []string{"share", "--period", "year"}, wantCode: 2, wantErr: "today, week, 30d, or all"},
		{name: "stray argument", args: []string{"share", "now"}, wantCode: 2, wantErr: "share accepts only --period"},
		{name: "unknown flag", args: []string{"share", "--post"}, wantCode: 2, wantErr: "flag provided but not defined"},
		{
			name: "unreadable local usage", args: []string{"share"}, wantCode: 1, wantErr: "could not read local usage; run `tokenholic doctor`",
			mutate: func(deps *dependencies) {
				deps.usage = func(context.Context, string, string) (application.Dashboard, error) {
					return application.Dashboard{}, errors.New("open /private/path/tokenholic.db: denied")
				}
			},
		},
		{
			name: "relative CODEX_HOME", args: []string{"share"}, wantCode: 1, wantErr: "CODEX_HOME must be an absolute directory",
			mutate: func(deps *dependencies) {
				deps.getenv = func(key string) string {
					if key == "CODEX_HOME" {
						return "relative"
					}
					return ""
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			deps := shareDependencies(manager, "")
			if test.mutate != nil {
				test.mutate(&deps)
			}
			var stdout, stderr bytes.Buffer
			code := run(test.args, &stdout, &stderr, deps)
			if code != test.wantCode || !strings.Contains(stderr.String(), test.wantErr) || strings.Contains(stderr.String(), "/private/path") {
				t.Fatalf("run(%v) = %d, stderr %q", test.args, code, stderr.String())
			}
			if strings.Contains(stdout.String(), "x.com") {
				t.Fatalf("a link was printed: %q", stdout.String())
			}
		})
	}
}
