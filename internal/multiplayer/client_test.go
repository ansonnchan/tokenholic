package multiplayer

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ansonnchan/tokenholic/internal/account"
)

func TestClientGroupAndLeaderboardProtocol(t *testing.T) {
	t.Parallel()
	groupID := "70000000-0000-4000-8000-000000000001"
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Header.Get("Authorization") != "Bearer device-credential" {
			t.Fatalf("authorization = %q", request.Header.Get("Authorization"))
		}
		status := http.StatusOK
		body := ""
		switch {
		case request.Method == http.MethodGet && request.URL.Path == "/v1/groups":
			body = `{"groups":[{"id":"` + groupID + `","name":"Quiet group","timezone":"UTC","role":"owner"}]}`
		case request.Method == http.MethodPost && request.URL.Path == "/v1/groups":
			var input map[string]string
			if err := json.NewDecoder(request.Body).Decode(&input); err != nil || input["timezone"] != "UTC" {
				t.Fatalf("create input = %#v, %v", input, err)
			}
			status = http.StatusCreated
			body = `{"id":"` + groupID + `","name":"Quiet group","timezone":"UTC","role":"owner"}`
		case request.Method == http.MethodPost && request.URL.Path == "/v1/group-invites/join":
			var input map[string]string
			if err := json.NewDecoder(request.Body).Decode(&input); err != nil || input["code"] != "ovt_synthetic" {
				t.Fatalf("join input = %#v, %v", input, err)
			}
			body = `{"id":"` + groupID + `","name":"Quiet group","timezone":"UTC","role":"member"}`
		case request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/invites"):
			status = http.StatusCreated
			body = `{"code":"ovt_invite","expires_at":"2026-11-19T00:00:00Z","max_uses":2}`
		case request.Method == http.MethodDelete && strings.HasSuffix(request.URL.Path, "/membership"):
			status = http.StatusNoContent
		case request.Method == http.MethodGet && strings.HasSuffix(request.URL.Path, "/leaderboard"):
			if request.URL.Query().Get("period") != "week" || request.URL.Query().Get("metric") != "api_estimate" {
				t.Fatalf("leaderboard query = %v", request.URL.Query())
			}
			body = `{"group_id":"` + groupID + `","group_name":"Quiet group","timezone":"UTC","period":"week","metric":"api_estimate","window":{"start":"2026-11-16T00:00:00Z","end":"2026-11-18T00:00:00Z"},"generated_at":"2026-11-18T00:00:00Z",` +
				`"comparison":{"kind":"previous_period","window":{"start":"2026-11-09T00:00:00Z","end":"2026-11-11T00:00:00Z"}},` +
				`"cost_rank_min_priced_basis_points":9500,"rows":[{"rank":null,"unranked_reason":"cost_coverage_below_threshold",` +
				`"user_id":"user","display_name":"Member","value":{"value":"12","state":"partial"},` +
				`"cost_coverage":{"priced_tokens":"94","unpriced_tokens":"6","priced_basis_points":9400},` +
				`"trend":{"previous_value":{"value":null,"state":"empty"},"previous_rank":null,"rank_change":null,"new":true},` +
				`"plausibility":{"daily_tokens_above_limit":true}}]}`
		default:
			status = http.StatusNotFound
		}
		return &http.Response{
			StatusCode: status, Header: make(http.Header),
			Body: io.NopCloser(strings.NewReader(body)), Request: request,
		}, nil
	})}
	client, err := NewClient("https://api.example.test", httpClient)
	if err != nil {
		t.Fatal(err)
	}
	groups, err := client.List(t.Context(), "device-credential")
	if err != nil || len(groups) != 1 || groups[0].Name != "Quiet group" {
		t.Fatalf("List() = %#v, %v", groups, err)
	}
	created, err := client.Create(t.Context(), "device-credential", "Quiet group", "UTC")
	if err != nil || created.ID != groupID {
		t.Fatalf("Create() = %#v, %v", created, err)
	}
	joined, err := client.Join(t.Context(), "device-credential", "ovt_synthetic")
	if err != nil || joined.Role != "member" {
		t.Fatalf("Join() = %#v, %v", joined, err)
	}
	invite, err := client.Invite(t.Context(), "device-credential", groupID, time.Hour, 2)
	if err != nil || invite.Code != "ovt_invite" || invite.MaxUses != 2 {
		t.Fatalf("Invite() = %#v, %v", invite, err)
	}
	if err := client.Leave(t.Context(), "device-credential", groupID); err != nil {
		t.Fatalf("Leave() error = %v", err)
	}
	board, err := client.Leaderboard(t.Context(), "device-credential", groupID, PeriodWeek, MetricAPIEstimate)
	if err != nil || board.Period != PeriodWeek || board.Metric != MetricAPIEstimate {
		t.Fatalf("Leaderboard() = %#v, %v", board, err)
	}
	if board.Comparison.Kind != ComparisonPreviousPeriod || board.CostRankMinPricedBasisPoints != 9500 || len(board.Rows) != 1 {
		t.Fatalf("board rules = %#v", board)
	}
	row := board.Rows[0]
	if row.Rank != nil || row.UnrankedReason != UnrankedCostCoverage || row.Coverage.PricedBasisPoints == nil || *row.Coverage.PricedBasisPoints != 9400 ||
		!row.Trend.New || row.Trend.PreviousValue.State != AvailabilityEmpty || !row.Plausibility.DailyTokensAboveLimit {
		t.Fatalf("decoded row = %#v", row)
	}
}

func TestClientReadsTheGlobalBoard(t *testing.T) {
	t.Parallel()
	const head = `"timezone":"UTC","period":"all_time","metric":"total_tokens","window":{"start":null,"end":"2026-11-21T00:00:00Z"},"generated_at":"2026-11-21T00:00:00Z",` +
		`"comparison":{"kind":"24h_earlier","window":{"start":null,"end":"2026-11-20T00:00:00Z"}},"cost_rank_min_priced_basis_points":9500,`
	responses := map[string]string{
		// Two members cannot be ranked against each other in public: the
		// server hides every rank and says so on the board and on each row.
		"hidden": `{"scope":"global",` + head + `"min_ranked_members":3,"ranks_hidden":true,"total_members":2,"rows":[` +
			`{"rank":null,"unranked_reason":"scope_too_small","user_id":"viewer","display_name":"viewer","current_user":true,"value":{"value":"900","state":"complete"},` +
			`"cost_coverage":{"priced_tokens":"900","unpriced_tokens":"0","priced_basis_points":10000},"trend":{"previous_value":{"value":"700","state":"complete"},"previous_rank":null,"rank_change":null,"new":false},"last_synced_at":null},` +
			`{"rank":null,"unranked_reason":"scope_too_small","user_id":"","display_name":"other","value":{"value":"400","state":"partial"},` +
			`"cost_coverage":{"priced_tokens":"0","unpriced_tokens":"400","priced_basis_points":0},"trend":{"previous_value":{"value":null,"state":"empty"},"new":true}}]}`,
		// A long board leaves rows out and still reports everyone it counted.
		"long":         `{"scope":"global",` + head + `"min_ranked_members":3,"ranks_hidden":false,"total_members":250,"rows":null}`,
		"group reply":  `{"scope":"group","group_id":"70000000-0000-4000-8000-000000000001","group_name":"Quiet group",` + head + `"rows":[]}`,
		"names group":  `{"scope":"global","group_id":"70000000-0000-4000-8000-000000000001",` + head + `"rows":[]}`,
		"other period": `{"scope":"global",` + strings.Replace(head, `"all_time"`, `"week"`, 1) + `"rows":[]}`,
		"no scope":     `{` + head + `"rows":[]}`,
	}
	served := "hidden"
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodGet || request.URL.Path != "/v1/global/leaderboard" || request.Header.Get("Authorization") != "Bearer device-credential" {
			t.Fatalf("request = %s %s with %q", request.Method, request.URL.Path, request.Header.Get("Authorization"))
		}
		if query := request.URL.Query(); query.Get("period") != "all_time" || query.Get("metric") != "total_tokens" || len(query) != 2 {
			t.Fatalf("query = %v", query)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(responses[served])), Request: request}, nil
	})}
	client, err := NewClient("https://api.example.test", httpClient)
	if err != nil {
		t.Fatal(err)
	}
	board, err := client.GlobalLeaderboard(t.Context(), "device-credential", PeriodAllTime, MetricTotalTokens)
	if err != nil {
		t.Fatal(err)
	}
	if board.Scope != ScopeGlobal || !board.RanksHidden || board.MinRankedMembers != 3 || board.TotalMembers != 2 || board.Comparison.Kind != ComparisonDayEarlier || len(board.Rows) != 2 {
		t.Fatalf("board = %#v", board)
	}
	viewer, other := board.Rows[0], board.Rows[1]
	if viewer.Rank != nil || viewer.UnrankedReason != UnrankedScopeTooSmall || !viewer.CurrentUser || viewer.UserID != "viewer" || viewer.LastSyncedAt != nil {
		t.Fatalf("viewer row = %#v", viewer)
	}
	// Another member's row carries no account ID, and a missing value stays
	// missing instead of becoming zero.
	if other.UserID != "" || other.CurrentUser || !other.Trend.New || other.Trend.PreviousValue.Value != nil || other.Value.State != AvailabilityPartial {
		t.Fatalf("other row = %#v", other)
	}
	served = "long"
	board, err = client.GlobalLeaderboard(t.Context(), "device-credential", PeriodAllTime, MetricTotalTokens)
	if err != nil || board.Rows == nil || len(board.Rows) != 0 || board.TotalMembers != 250 || board.RanksHidden {
		t.Fatalf("long board = %#v, %v", board, err)
	}
	for _, name := range []string{"group reply", "names group", "other period", "no scope"} {
		served = name
		if board, err := client.GlobalLeaderboard(t.Context(), "device-credential", PeriodAllTime, MetricTotalTokens); err == nil || len(board.Rows) != 0 || board.Scope != "" {
			t.Errorf("%s: GlobalLeaderboard() = %#v, %v; want a rejected response", name, board, err)
		}
	}
}

func TestServiceLoadsTheGlobalBoardOnlyWhenAsked(t *testing.T) {
	t.Parallel()
	client := &fakeClient{groups: []Group{{ID: "group-one", Name: "One", Timezone: "UTC", Role: "owner"}}}
	service, err := NewService(client, fakeStore{session: account.Session{Credential: "device"}}, "UTC")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := service.Load(t.Context(), "", PeriodWeek, MetricAPIEstimate)
	if err != nil || snapshot.Global || snapshot.Selected.ID != "group-one" || client.globalReads != 0 {
		t.Fatalf("default Load() = %#v, %v after %d global reads", snapshot, err, client.globalReads)
	}
	snapshot, err = service.Load(t.Context(), GlobalScope, PeriodWeek, MetricAPIEstimate)
	if err != nil || !snapshot.Global || snapshot.Selected.ID != "" || snapshot.Board == nil || snapshot.Board.Scope != ScopeGlobal || !snapshot.Board.RanksHidden || len(snapshot.Groups) != 1 {
		t.Fatalf("global Load() = %#v, %v", snapshot, err)
	}
	// An account with no groups can still read the global board.
	client.groups = nil
	if snapshot, err = service.Load(t.Context(), GlobalScope, PeriodWeek, MetricAPIEstimate); err != nil || snapshot.Board == nil {
		t.Fatalf("global Load() without groups = %#v, %v", snapshot, err)
	}
	if snapshot, err = service.Load(t.Context(), "", PeriodWeek, MetricAPIEstimate); err != nil || snapshot.Board != nil || snapshot.Global {
		t.Fatalf("Load() without groups = %#v, %v", snapshot, err)
	}
	// An expired session reads as signed out, as it does for a group.
	client.globalErr = ErrUnauthorized
	if _, err = service.Load(t.Context(), GlobalScope, PeriodWeek, MetricAPIEstimate); !errors.Is(err, ErrSignedOut) {
		t.Fatalf("global Load() with an expired session = %v", err)
	}
}

func TestClientRejectsInsecureRemoteOriginAndMapsAuthorization(t *testing.T) {
	t.Parallel()
	if _, err := NewClient("http://example.test", nil); err == nil {
		t.Fatal("NewClient() accepted remote plaintext HTTP")
	}
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusUnauthorized, Header: make(http.Header),
			Body: io.NopCloser(strings.NewReader("")), Request: request,
		}, nil
	})}
	client, err := NewClient("https://api.example.test", httpClient)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.List(t.Context(), "expired"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("List() error = %v", err)
	}
}

func TestServiceLoadsSelectionAndResolvesGroupCommands(t *testing.T) {
	t.Parallel()
	client := &fakeClient{groups: []Group{
		{ID: "group-one", Name: "One", Timezone: "UTC", Role: "owner"},
		{ID: "group-two", Name: "Two", Timezone: "UTC", Role: "member"},
	}}
	service, err := NewService(client, fakeStore{session: account.Session{Credential: "device"}}, "UTC")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := service.Load(t.Context(), "group-two", PeriodToday, MetricTotalTokens)
	if err != nil || snapshot.Selected.ID != "group-two" || client.boardGroup != "group-two" {
		t.Fatalf("Load() = %#v, %v, group=%q", snapshot, err, client.boardGroup)
	}
	created, err := service.Create(t.Context(), "New")
	if err != nil || created.Timezone != "UTC" || client.createdTimezone != "UTC" {
		t.Fatalf("Create() = %#v, %v", created, err)
	}
	if _, err := service.Join(t.Context(), "ovt_join"); err != nil || client.joinedCode != "ovt_join" {
		t.Fatalf("Join() error/code = %v/%q", err, client.joinedCode)
	}
	if _, err := service.Invite(t.Context(), "One", time.Hour, 2); err != nil || client.inviteGroup != "group-one" {
		t.Fatalf("Invite() error/group = %v/%q", err, client.inviteGroup)
	}
	if err := service.Leave(t.Context(), "Two"); err != nil || client.leftGroup != "group-two" {
		t.Fatalf("Leave() error/group = %v/%q", err, client.leftGroup)
	}
	for _, participating := range []bool{true, false} {
		if err := service.SetGlobalParticipation(t.Context(), participating); err != nil || client.participating == nil || *client.participating != participating {
			t.Fatalf("SetGlobalParticipation(%t) = %v, sent %v", participating, err, client.participating)
		}
	}
	// Lifecycle commands resolve a group by name before acting on its ID.
	if group, err := service.SetGroupVisibility(t.Context(), "One", true); err != nil || client.visibilityGroup != "group-one" || group.PublicSlug == "" {
		t.Fatalf("SetGroupVisibility() = %#v, %v, group %q", group, err, client.visibilityGroup)
	}
	if err := service.TransferGroup(t.Context(), "One", "new-owner"); err != nil || client.transferGroup != "group-one" || client.transferLogin != "new-owner" {
		t.Fatalf("TransferGroup() = %v, %q to %q", err, client.transferGroup, client.transferLogin)
	}
	if err := service.DeleteGroup(t.Context(), "Unknown"); !errors.Is(err, ErrNotFound) || client.deletedGroup != "" {
		t.Fatalf("DeleteGroup(unknown) = %v, deleted %q", err, client.deletedGroup)
	}
	if err := service.DeleteGroup(t.Context(), "Two"); err != nil || client.deletedGroup != "group-two" {
		t.Fatalf("DeleteGroup() = %v, deleted %q", err, client.deletedGroup)
	}
	if profile, err := service.Profile(t.Context()); err != nil || profile.Login != "member" {
		t.Fatalf("Profile() = %#v, %v", profile, err)
	}
	if err := service.SetProfileVisibility(t.Context(), true); err != nil || client.profilePublic == nil || !*client.profilePublic {
		t.Fatalf("SetProfileVisibility() = %v, sent %v", err, client.profilePublic)
	}
}

// The client reports a visibility change only when the server's reply says
// the same thing, and sends the confirmation word a deletion requires.
func TestClientProfileAndGroupLifecycleRequests(t *testing.T) {
	t.Parallel()
	const groupID = "10000000-0000-4000-8000-000000000001"
	replies := map[string]string{
		"GET /v1/account/profile":                   `{"login":"member","visibility":"private","global_opt_in":false}`,
		"PUT /v1/account/profile":                   `{"visibility":"public"}`,
		"PUT /v1/groups/" + groupID + "/visibility": `{"id":"` + groupID + `","name":"Quiet group","timezone":"UTC","role":"owner","visibility":"public","public_slug":"g-publicab"}`,
		"PUT /v1/groups/" + groupID + "/owner":      ``,
		"DELETE /v1/groups/" + groupID:              ``,
	}
	sent := map[string]string{}
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		key := request.Method + " " + request.URL.Path
		reply, known := replies[key]
		if !known || request.Header.Get("Authorization") != "Bearer device-credential" {
			t.Fatalf("unexpected request %s", key)
		}
		if request.Body != nil {
			body, _ := io.ReadAll(request.Body)
			sent[key] = string(body)
		}
		status := http.StatusOK
		if reply == "" {
			status = http.StatusNoContent
		}
		return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(reply)), Request: request}, nil
	})}
	client, err := NewClient("https://api.example.test", httpClient)
	if err != nil {
		t.Fatal(err)
	}
	if profile, err := client.Profile(t.Context(), "device-credential"); err != nil || profile.Login != "member" || profile.Visibility != "private" {
		t.Fatalf("Profile() = %#v, %v", profile, err)
	}
	if err := client.SetProfileVisibility(t.Context(), "device-credential", true); err != nil {
		t.Fatalf("SetProfileVisibility(public) = %v", err)
	}
	// The server still says public, so going private is not confirmed.
	if err := client.SetProfileVisibility(t.Context(), "device-credential", false); err == nil {
		t.Fatal("SetProfileVisibility(private) reported a change the server did not confirm")
	}
	if group, err := client.SetGroupVisibility(t.Context(), "device-credential", groupID, true); err != nil || group.PublicSlug != "g-publicab" {
		t.Fatalf("SetGroupVisibility(public) = %#v, %v", group, err)
	}
	if _, err := client.SetGroupVisibility(t.Context(), "device-credential", groupID, false); err == nil {
		t.Fatal("SetGroupVisibility(private) reported a change the server did not confirm")
	}
	if err := client.TransferGroup(t.Context(), "device-credential", groupID, "new-owner"); err != nil || sent["PUT /v1/groups/"+groupID+"/owner"] != `{"login":"new-owner"}` {
		t.Fatalf("TransferGroup() = %v after sending %q", err, sent["PUT /v1/groups/"+groupID+"/owner"])
	}
	if err := client.DeleteGroup(t.Context(), "device-credential", groupID); err != nil || sent["DELETE /v1/groups/"+groupID] != `{"confirmation":"DELETE"}` {
		t.Fatalf("DeleteGroup() = %v after sending %q", err, sent["DELETE /v1/groups/"+groupID])
	}
}

func TestClientSetsGlobalParticipationOnlyWhenTheServerConfirms(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		reply   string
		status  int
		wantErr bool
	}{
		{name: "confirmed", reply: `{"participating":true}`, status: http.StatusOK},
		{name: "server reports the opposite", reply: `{"participating":false}`, status: http.StatusOK, wantErr: true},
		{name: "server confirms nothing", reply: `{}`, status: http.StatusOK, wantErr: true},
		{name: "rejected", reply: `{"error":"invalid_request"}`, status: http.StatusBadRequest, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var sent string
			httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				if request.Method != http.MethodPut || request.URL.Path != "/v1/account/global-board" || request.Header.Get("Authorization") != "Bearer device-credential" {
					t.Fatalf("request = %s %s", request.Method, request.URL.Path)
				}
				body, _ := io.ReadAll(request.Body)
				sent = string(body)
				return &http.Response{StatusCode: test.status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(test.reply)), Request: request}, nil
			})}
			client, err := NewClient("https://api.example.test", httpClient)
			if err != nil {
				t.Fatal(err)
			}
			err = client.SetGlobalParticipation(t.Context(), "device-credential", true)
			if (err != nil) != test.wantErr || sent != `{"participating":true}` {
				t.Fatalf("SetGlobalParticipation() error = %v after sending %q", err, sent)
			}
		})
	}
}

func TestServiceRequiresStoredSession(t *testing.T) {
	t.Parallel()
	service, err := NewService(&fakeClient{}, fakeStore{err: account.ErrNoSession}, "UTC")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Load(t.Context(), "", PeriodToday, MetricTotalTokens); !errors.Is(err, ErrSignedOut) {
		t.Fatalf("Load() error = %v", err)
	}
}

type fakeStore struct {
	session account.Session
	err     error
}

func (f fakeStore) Load() (account.Session, error) { return f.session, f.err }

type fakeClient struct {
	groups          []Group
	boardGroup      string
	globalReads     int
	globalErr       error
	createdTimezone string
	inviteGroup     string
	leftGroup       string
	joinedCode      string
	participating   *bool
	visibilityGroup string
	deletedGroup    string
	transferGroup   string
	transferLogin   string
	profilePublic   *bool
}

func (f *fakeClient) Profile(context.Context, string) (Profile, error) {
	return Profile{Login: "member", Visibility: "private"}, nil
}
func (f *fakeClient) SetProfileVisibility(_ context.Context, _ string, public bool) error {
	f.profilePublic = &public
	return nil
}
func (f *fakeClient) SetGroupVisibility(_ context.Context, _, groupID string, public bool) (Group, error) {
	f.visibilityGroup = groupID
	return Group{ID: groupID, Visibility: visibilityName(public), PublicSlug: "g-publicab"}, nil
}
func (f *fakeClient) DeleteGroup(_ context.Context, _, groupID string) error {
	f.deletedGroup = groupID
	return nil
}
func (f *fakeClient) TransferGroup(_ context.Context, _, groupID, login string) error {
	f.transferGroup, f.transferLogin = groupID, login
	return nil
}

func (f *fakeClient) SetGlobalParticipation(_ context.Context, _ string, participating bool) error {
	f.participating = &participating
	return nil
}

func (f *fakeClient) List(context.Context, string) ([]Group, error) { return f.groups, nil }
func (f *fakeClient) Create(_ context.Context, _, name, timezone string) (Group, error) {
	f.createdTimezone = timezone
	return Group{ID: "created", Name: name, Timezone: timezone, Role: "owner"}, nil
}
func (f *fakeClient) Join(_ context.Context, _, code string) (Group, error) {
	f.joinedCode = code
	return Group{ID: "joined"}, nil
}
func (f *fakeClient) Invite(_ context.Context, _, groupID string, _ time.Duration, _ int) (Invite, error) {
	f.inviteGroup = groupID
	return Invite{Code: "ovt_synthetic"}, nil
}
func (f *fakeClient) Leave(_ context.Context, _, groupID string) error {
	f.leftGroup = groupID
	return nil
}
func (f *fakeClient) Leaderboard(_ context.Context, _, groupID string, period Period, metric Metric) (Board, error) {
	f.boardGroup = groupID
	return Board{Scope: ScopeGroup, GroupID: groupID, Period: period, Metric: metric}, nil
}

func (f *fakeClient) GlobalLeaderboard(_ context.Context, _ string, period Period, metric Metric) (Board, error) {
	f.globalReads++
	if f.globalErr != nil {
		return Board{}, f.globalErr
	}
	return Board{Scope: ScopeGlobal, Period: period, Metric: metric, RanksHidden: true}, nil
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}
