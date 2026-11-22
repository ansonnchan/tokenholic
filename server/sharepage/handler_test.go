package sharepage

import (
	"bytes"
	"context"
	"errors"
	"image/png"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	serveraccount "github.com/ansonnchan/tokenholic/server/account"
	servergroup "github.com/ansonnchan/tokenholic/server/group"
	serverleaderboard "github.com/ansonnchan/tokenholic/server/leaderboard"
	"github.com/ansonnchan/tokenholic/server/sharecard"
	"github.com/ansonnchan/tokenholic/server/website"
)

func value(amount string, state serveraccount.Availability) serveraccount.Value {
	if amount == "" {
		return serveraccount.Value{State: state}
	}
	return serveraccount.Value{Value: &amount, State: state}
}

func rank(number int) *int { return &number }

// fakeProfiles knows one public profile. A private account and one that
// never existed both get the single not-found error, as the real service
// returns them.
type fakeProfiles struct{ err error }

func (f fakeProfiles) PublicProfile(_ context.Context, login string) (serveraccount.PublicProfile, error) {
	if f.err != nil {
		return serveraccount.PublicProfile{}, f.err
	}
	if login != "dana" {
		return serveraccount.PublicProfile{}, serveraccount.ErrProfileNotFound
	}
	return serveraccount.PublicProfile{
		Login: "dana",
		Last30Days: serveraccount.PublicTotals{
			TotalTokens: value("4263000000", serveraccount.AvailabilityPartial), APIEstimate: value("1180420000", serveraccount.AvailabilityPartial),
			PricedTokens: "3934749000", UnpricedTokens: "328251000",
		},
		AllTime: serveraccount.PublicTotals{
			TotalTokens: value("21870000000", serveraccount.AvailabilityPartial), APIEstimate: value("6204180000", serveraccount.AvailabilityPartial),
			PricedTokens: "20623410000", UnpricedTokens: "1246590000",
		},
	}, nil
}

type fakeBoards struct {
	period serverleaderboard.Period
	metric serverleaderboard.Metric
	name   string
}

func (f *fakeBoards) PublicGroup(_ context.Context, slug string, period serverleaderboard.Period, metric serverleaderboard.Metric) (serverleaderboard.Board, error) {
	f.period, f.metric = period, metric
	if slug != "g-nightshift" {
		return serverleaderboard.Board{}, servergroup.ErrNotFound
	}
	name := f.name
	if name == "" {
		name = "night-shift"
	}
	return serverleaderboard.Board{
		Scope: serverleaderboard.ScopePublicGroup, GroupName: name, PublicSlug: slug, Period: period, Metric: metric,
		MinRankedMembers: 3, TotalMembers: 9,
		Rows: []serverleaderboard.Row{
			{Rank: rank(1), DisplayName: "dana", Value: value("1180000000", serveraccount.AvailabilityComplete)},
			{Rank: rank(2), DisplayName: "kai", Value: value("1090000000", serveraccount.AvailabilityComplete)},
			{Rank: rank(3), DisplayName: "rio", Value: value("840500000", serveraccount.AvailabilityComplete)},
			{Rank: rank(4), DisplayName: "mika", Value: value("702150000", serveraccount.AvailabilityComplete)},
		},
	}, nil
}

func newHandler(t *testing.T, profiles Profiles, boards Boards) *Handler {
	t.Helper()
	cards, err := sharecard.New()
	if err != nil {
		t.Fatal(err)
	}
	site, err := website.Parse("https://pages.example/tokenholic")
	if err != nil {
		t.Fatal(err)
	}
	handler, err := New(profiles, boards, cards, site)
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func get(handler http.Handler, path string, headers ...string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodGet, "https://api.tokenholic.example"+path, nil)
	for index := 0; index+1 < len(headers); index += 2 {
		request.Header.Set(headers[index], headers[index+1])
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func TestProfileSharePageCarriesOpenGraphTagsAndSendsBrowsersOn(t *testing.T) {
	t.Parallel()
	response := get(newHandler(t, fakeProfiles{}, &fakeBoards{}), "/s/u/dana")
	body := response.Body.String()
	if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "text/html; charset=utf-8" || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("share page = %d %v", response.Code, response.Header())
	}
	for _, want := range []string{
		`<title>@dana on tokenholic</title>`,
		`<meta property="og:title" content="@dana on tokenholic">`,
		`<meta property="og:description" content="At least 4.26B tokens with coding agents in the last 30 days. At least $1,180.42 at API list prices (an estimate, never spending).">`,
		`<meta property="og:url" content="https://api.tokenholic.example/s/u/dana">`,
		`<meta property="og:image" content="https://api.tokenholic.example/s/u/dana/card.png?v=`,
		`<meta property="og:image:width" content="1200">`,
		`<meta property="og:image:height" content="630">`,
		`<meta name="twitter:card" content="summary_large_image">`,
		`<meta http-equiv="refresh" content="0; url=https://pages.example/tokenholic/u/?login=dana">`,
		`<a href="https://pages.example/tokenholic/u/?login=dana">`,
		`<meta name="robots" content="noindex">`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("share page lacks %s", want)
		}
	}
	if strings.Contains(body, "<script") {
		t.Error("share page contains a script")
	}
	if policy := response.Header().Get("Content-Security-Policy"); !strings.Contains(policy, "default-src 'none'") || strings.Contains(policy, "script-src") {
		t.Errorf("Content-Security-Policy = %q", policy)
	}
}

func TestCardRouteReturnsACacheableLargeImage(t *testing.T) {
	t.Parallel()
	handler := newHandler(t, fakeProfiles{}, &fakeBoards{})
	for _, path := range []string{"/s/u/dana/card.png", "/s/b/g-nightshift/card.png", "/s/u/dana/card.png?v=stale"} {
		response := get(handler, path)
		if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "image/png" {
			t.Fatalf("GET %s = %d %q", path, response.Code, response.Header().Get("Content-Type"))
		}
		config, err := png.DecodeConfig(bytes.NewReader(response.Body.Bytes()))
		if err != nil || config.Width != 1200 || config.Height != 630 {
			t.Fatalf("GET %s card = %dx%d, %v", path, config.Width, config.Height, err)
		}
		tag := response.Header().Get("ETag")
		if len(tag) != 18 || response.Header().Get("Cache-Control") != "public, max-age=300" {
			t.Fatalf("GET %s caching = ETag %q, Cache-Control %q", path, tag, response.Header().Get("Cache-Control"))
		}
		again := get(handler, path, "If-None-Match", tag)
		if again.Code != http.StatusNotModified || again.Body.Len() != 0 || again.Header().Get("ETag") != tag {
			t.Fatalf("revalidating %s = %d with %d bytes", path, again.Code, again.Body.Len())
		}
	}
	// The page's image address carries the card's own entity tag.
	page := get(handler, "/s/u/dana").Body.String()
	tag := strings.Trim(get(handler, "/s/u/dana/card.png").Header().Get("ETag"), `"`)
	if !strings.Contains(page, "/s/u/dana/card.png?v="+tag+`"`) {
		t.Errorf("page does not link the card as version %q", tag)
	}
}

// Every miss under the share prefix is one response: same status, headers,
// and body for a private record, a missing one, a malformed name, and an
// unknown path, for pages and cards alike.
func TestEveryShareMissIsIdentical(t *testing.T) {
	t.Parallel()
	handler := newHandler(t, fakeProfiles{}, &fakeBoards{})
	misses := []string{
		"/s/u/private-member", "/s/u/nobody", "/s/u/private-member/card.png", "/s/u/nobody/card.png",
		"/s/u/not_a_login", "/s/u/-", "/s/u/dana/other.png", "/s/u/dana/card.png/extra",
		"/s/b/g-privatea", "/s/b/g-nothere", "/s/b/g-privatea/card.png", "/s/b/g-nothere/card.png",
		"/s/b/g-privatea?period=all_time&metric=total_tokens", "/s/b/NOT-A-SLUG",
		"/s/", "/s/x", "/s/u/", "/s/g/whatever",
	}
	reference := get(handler, misses[0])
	if reference.Code != http.StatusNotFound || !strings.Contains(reference.Body.String(), "NOT FOUND") || reference.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("miss = %d %v", reference.Code, reference.Header())
	}
	if reference.Header().Get("ETag") != "" || strings.Contains(reference.Body.String(), "private-member") {
		t.Fatalf("miss leaks detail: %v %q", reference.Header(), reference.Body.String())
	}
	for _, path := range misses[1:] {
		response := get(handler, path)
		if response.Code != reference.Code || response.Body.String() != reference.Body.String() || !reflect.DeepEqual(response.Header(), reference.Header()) {
			t.Errorf("GET %s = %d %v, want exactly the reference miss %v", path, response.Code, response.Header(), reference.Header())
		}
	}
	// Revalidating with a real card's tag reveals nothing about a miss.
	tag := get(handler, "/s/u/dana/card.png").Header().Get("ETag")
	if response := get(handler, "/s/u/private-member/card.png", "If-None-Match", tag); response.Code != http.StatusNotFound || response.Body.String() != reference.Body.String() {
		t.Errorf("conditional request for a miss = %d", response.Code)
	}
}

func TestBoardSharePageOpensAsTheWebsiteBoardDoes(t *testing.T) {
	t.Parallel()
	boards := &fakeBoards{}
	handler := newHandler(t, fakeProfiles{}, boards)
	tests := []struct {
		query          string
		period, metric string
	}{
		{"", "week", "api_estimate"},
		{"?period=all_time&metric=total_tokens", "all_time", "total_tokens"},
		{"?period=tomorrow&metric=vibes", "week", "api_estimate"},
		{"?metric=total_tokens", "week", "total_tokens"},
	}
	for _, test := range tests {
		response := get(handler, "/s/b/g-nightshift"+test.query)
		body := response.Body.String()
		if response.Code != http.StatusOK || string(boards.period) != test.period || string(boards.metric) != test.metric {
			t.Fatalf("GET %q = %d, read %s by %s", test.query, response.Code, boards.period, boards.metric)
		}
		for _, want := range []string{
			`<meta property="og:title" content="night-shift on tokenholic">`,
			`content="https://api.tokenholic.example/s/b/g-nightshift?period=` + test.period + `&amp;metric=` + test.metric + `"`,
			`/s/b/g-nightshift/card.png?v=`,
			`&amp;period=` + test.period + `&amp;metric=` + test.metric + `">`,
			`url=https://pages.example/tokenholic/b/?metric=` + test.metric + `&amp;period=` + test.period + `&amp;slug=g-nightshift"`,
		} {
			if !strings.Contains(body, want) {
				t.Errorf("GET %q lacks %s", test.query, want)
			}
		}
	}
	body := get(handler, "/s/b/g-nightshift").Body.String()
	want := `content="This week by API-equivalent estimate: 1. @dana $1,180.00, 2. @kai $1,090.00, 3. @rio $840.50. Estimates are at API list prices, never spending."`
	if !strings.Contains(body, want) {
		t.Errorf("board description is not %s", want)
	}
}

func TestOwnerChosenTextCannotAddMarkup(t *testing.T) {
	t.Parallel()
	handler := newHandler(t, fakeProfiles{}, &fakeBoards{name: `"><script>alert(1)</script><meta http-equiv="refresh" content="0;url=https://evil.example">` + "\x00\x1b[31m"})
	response := get(handler, "/s/b/g-nightshift")
	body := response.Body.String()
	if response.Code != http.StatusOK || strings.Contains(body, "<script") || strings.Contains(body, "\x1b") || strings.Count(body, `http-equiv="refresh"`) != 1 {
		t.Fatalf("group name reached the page as markup:\n%s", body)
	}
	if !strings.Contains(body, "&lt;script&gt;") {
		t.Error("group name is missing from the page altogether")
	}
	if card := get(handler, "/s/b/g-nightshift/card.png"); card.Code != http.StatusOK {
		t.Errorf("card for an awkward group name = %d", card.Code)
	}
}

func TestAbsoluteLinksComeFromAPlainHostOnly(t *testing.T) {
	t.Parallel()
	handler := newHandler(t, fakeProfiles{}, &fakeBoards{})
	for host, origin := range map[string]string{
		"api.tokenholic.example": "https://api.tokenholic.example", "api.tokenholic.example:8443": "https://api.tokenholic.example:8443",
		"localhost:8080": "http://localhost:8080", "127.0.0.1:8080": "http://127.0.0.1:8080",
	} {
		request := httptest.NewRequest(http.MethodGet, "/s/u/dana", nil)
		request.Host = host
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if !strings.Contains(response.Body.String(), `content="`+origin+`/s/u/dana"`) {
			t.Errorf("Host %q did not produce origin %q", host, origin)
		}
	}
	miss := get(handler, "/s/u/nobody")
	for _, host := range []string{"", `evil.example"><script>`, "evil.example/path", "user@evil.example", "evil example", "[::1]:80", "-bad.example", "a.example:999999"} {
		request := httptest.NewRequest(http.MethodGet, "/s/u/dana", nil)
		request.Host = host
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusNotFound || response.Body.String() != miss.Body.String() {
			t.Errorf("Host %q = %d, want the ordinary miss", host, response.Code)
		}
	}
}

func TestShareRoutesAreReadOnlyAndHideFailures(t *testing.T) {
	t.Parallel()
	handler := newHandler(t, fakeProfiles{}, &fakeBoards{})
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(method, "/s/u/dana", strings.NewReader("{}")))
		if response.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s = %d, want 405", method, response.Code)
		}
	}
	failing := newHandler(t, fakeProfiles{err: errors.New("pq: connection to db-internal-7 refused")}, &fakeBoards{})
	for _, path := range []string{"/s/u/dana", "/s/u/dana/card.png"} {
		response := get(failing, path)
		if response.Code != http.StatusInternalServerError || strings.Contains(response.Body.String(), "db-internal") || response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("ETag") != "" {
			t.Errorf("GET %s on failure = %d %q %v", path, response.Code, response.Body.String(), response.Header())
		}
	}
	if _, err := New(nil, &fakeBoards{}, nil, nil); err == nil {
		t.Error("New accepted missing dependencies")
	}
}
