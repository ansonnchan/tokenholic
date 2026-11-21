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
	createdTimezone string
	inviteGroup     string
	leftGroup       string
	joinedCode      string
	participating   *bool
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
	return Board{GroupID: groupID, Period: period, Metric: metric}, nil
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}
