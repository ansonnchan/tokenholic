package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	protocol "github.com/ansonnchan/tokenholic/internal/sync"
	serveraccount "github.com/ansonnchan/tokenholic/server/account"
	serverauth "github.com/ansonnchan/tokenholic/server/auth"
	servergroup "github.com/ansonnchan/tokenholic/server/group"
	serverleaderboard "github.com/ansonnchan/tokenholic/server/leaderboard"
	serversync "github.com/ansonnchan/tokenholic/server/sync"
)

func TestAuthenticationRoutesAndStableErrors(t *testing.T) {
	service := &fakeAuthService{
		start: serverauth.StartResponse{
			FlowToken: "sealed", UserCode: "ABCD", VerificationURI: "https://github.com/login/device",
			ExpiresIn: 15 * time.Minute, Interval: 5 * time.Second,
		},
		poll:    serverauth.PollResponse{Status: serverauth.FlowPending, RetryAfter: 7 * time.Second},
		refresh: serverauth.Session{Credential: "replacement", DeviceID: "device", UserID: "user"},
	}
	handler, err := New(service, &fakeUsageSyncService{}, &fakeAccountUsageService{}, &fakeGroupService{}, &fakeLeaderboardService{})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name       string
		path       string
		body       string
		credential string
		wantStatus int
		wantBody   string
	}{
		{name: "start", path: "/v1/auth/device/start", body: `{"client_version":"test"}`, wantStatus: http.StatusCreated, wantBody: `"flow_token":"sealed"`},
		{name: "unknown field", path: "/v1/auth/device/start", body: `{"private_path":"no"}`, wantStatus: http.StatusBadRequest, wantBody: "invalid_request"},
		{name: "pending", path: "/v1/auth/device/poll", body: `{"flow_token":"sealed"}`, wantStatus: http.StatusAccepted, wantBody: "pending"},
		{name: "refresh", path: "/v1/auth/refresh", credential: "old", wantStatus: http.StatusOK, wantBody: "replacement"},
		{name: "refresh missing bearer", path: "/v1/auth/refresh", wantStatus: http.StatusUnauthorized, wantBody: "unauthorized"},
		{name: "logout", path: "/v1/auth/logout", credential: "old", wantStatus: http.StatusNoContent},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, test.path, strings.NewReader(test.body))
			if test.credential != "" {
				request.Header.Set("Authorization", "Bearer "+test.credential)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.wantStatus || !strings.Contains(response.Body.String(), test.wantBody) {
				t.Fatalf("response = %d %q", response.Code, response.Body.String())
			}
			if response.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("cache header = %q", response.Header().Get("Cache-Control"))
			}
		})
	}
}

func TestHandlerRedactsServiceFailures(t *testing.T) {
	service := &fakeAuthService{startErr: errors.New("provider token and private/path")}
	handler, err := New(service, &fakeUsageSyncService{}, &fakeAccountUsageService{}, &fakeGroupService{}, &fakeLeaderboardService{})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/auth/device/start", bytes.NewBufferString(`{"client_version":"test"}`))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadGateway || strings.Contains(response.Body.String(), "provider token") || strings.Contains(response.Body.String(), "private/path") {
		t.Fatalf("response = %d %q", response.Code, response.Body.String())
	}
}

func TestSyncRouteAuthenticatesAndReturnsExactAcknowledgement(t *testing.T) {
	service := &fakeAuthService{}
	syncService := &fakeUsageSyncService{}
	handler, err := New(service, syncService, &fakeAccountUsageService{}, &fakeGroupService{}, &fakeLeaderboardService{})
	if err != nil {
		t.Fatal(err)
	}
	batch := validSyncBatch()
	body, err := json.Marshal(batch)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/v2/sync/buckets", bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer device-credential")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || syncService.credential != "device-credential" || !strings.Contains(response.Body.String(), batch.Buckets[0].BucketKey) {
		t.Fatalf("sync response/service = %d %q/%#v", response.Code, response.Body.String(), syncService)
	}
}

func TestSyncRouteRejectsServerValidationFailures(t *testing.T) {
	handler, err := New(
		&fakeAuthService{}, &fakeUsageSyncService{err: serversync.ErrInvalidBatch},
		&fakeAccountUsageService{}, &fakeGroupService{}, &fakeLeaderboardService{},
	)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(validSyncBatch())
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/v2/sync/buckets", bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer device-credential")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "invalid_batch") {
		t.Fatalf("response = %d %q", response.Code, response.Body.String())
	}
}

// A 0.1.x client posts protocol v1. It must be told to upgrade rather than see
// a validation or authentication error it cannot act on.
func TestRetiredSyncV1RouteRequiresUpgrade(t *testing.T) {
	syncService := &fakeUsageSyncService{}
	handler, err := New(&fakeAuthService{}, syncService, &fakeAccountUsageService{}, &fakeGroupService{}, &fakeLeaderboardService{})
	if err != nil {
		t.Fatal(err)
	}
	v1Body := `{"schema_version":1,"buckets":[{"bucket_key":"` + strings.Repeat("a", 64) + `","bucket":{"canonical_model_id":"client-resolved"}}]}`
	tests := []struct {
		name, credential, body string
	}{
		{name: "signed-in v1 client", credential: "Bearer device-credential", body: v1Body},
		{name: "no credential", body: v1Body},
		{name: "unreadable body", credential: "Bearer device-credential", body: "{"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/v1/sync/buckets", strings.NewReader(test.body))
			if test.credential != "" {
				request.Header.Set("Authorization", test.credential)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusUpgradeRequired || response.Header().Get("Upgrade") != "tokenholic-sync/2" {
				t.Fatalf("response = %d upgrade=%q, want 426 naming the required protocol", response.Code, response.Header().Get("Upgrade"))
			}
			var body map[string]string
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body["error"] != "upgrade_required" || !strings.Contains(body["message"], "npx @tokenholic/cli@latest") {
				t.Fatalf("body = %#v, want an upgrade instruction", body)
			}
		})
	}
	if syncService.credential != "" {
		t.Fatal("a retired-protocol upload reached the synchronization service")
	}
}

func TestSyncRouteRejectsV1PayloadOnTheV2Route(t *testing.T) {
	syncService := &fakeUsageSyncService{}
	handler, err := New(&fakeAuthService{}, syncService, &fakeAccountUsageService{}, &fakeGroupService{}, &fakeLeaderboardService{})
	if err != nil {
		t.Fatal(err)
	}
	// Client-resolved fields are unknown to v2 and fail strict decoding.
	batch := validSyncBatch()
	encoded, err := json.Marshal(batch)
	if err != nil {
		t.Fatal(err)
	}
	withClientResolution := strings.Replace(string(encoded), `"model_evidence"`, `"canonical_model_id":"client-resolved","model_evidence"`, 1)
	request := httptest.NewRequest(http.MethodPost, "/v2/sync/buckets", strings.NewReader(withClientResolution))
	request.Header.Set("Authorization", "Bearer device-credential")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || syncService.credential != "" {
		t.Fatalf("response = %d %q, want 400 before the service", response.Code, response.Body.String())
	}
}

func TestAccountUsageRouteRequiresCredential(t *testing.T) {
	accountService := &fakeAccountUsageService{usage: serveraccount.Usage{
		UserID: "user", DisplayName: "synthetic-user", GeneratedAt: time.Now().UTC(),
	}}
	handler, err := New(&fakeAuthService{}, &fakeUsageSyncService{}, accountService, &fakeGroupService{}, &fakeLeaderboardService{})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/v1/account/usage", nil)
	request.Header.Set("Authorization", "Bearer device-credential")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || accountService.credential != "device-credential" || !strings.Contains(response.Body.String(), "synthetic-user") {
		t.Fatalf("account response = %d %q/%#v", response.Code, response.Body.String(), accountService)
	}
	request = httptest.NewRequest(http.MethodGet, "/v1/account/usage", nil)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized account response = %d %q", response.Code, response.Body.String())
	}
}

func TestAccountDeletionRequiresExactConfirmation(t *testing.T) {
	accountService := &fakeAccountUsageService{}
	handler, err := New(&fakeAuthService{}, &fakeUsageSyncService{}, accountService, &fakeGroupService{}, &fakeLeaderboardService{})
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{"", `{}`, `{"confirmation":"delete"}`} {
		request := httptest.NewRequest(http.MethodDelete, "/v1/account", strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer device")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest || accountService.deleted {
			t.Fatalf("unconfirmed deletion = %d %q/%v", response.Code, response.Body.String(), accountService.deleted)
		}
	}
	request := httptest.NewRequest(http.MethodDelete, "/v1/account", strings.NewReader(`{"confirmation":"DELETE"}`))
	request.Header.Set("Authorization", "Bearer device")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent || !accountService.deleted || accountService.credential != "device" {
		t.Fatalf("confirmed deletion = %d %q/%#v", response.Code, response.Body.String(), accountService)
	}
}

func TestGroupRoutesPreserveAuthorizationAndStableErrors(t *testing.T) {
	groupID := "10000000-0000-4000-8000-000000000001"
	userID := "10000000-0000-4000-8000-000000000002"
	groups := &fakeGroupService{
		group:  servergroup.Group{ID: groupID, Name: "Private group", Timezone: "America/Vancouver", Role: servergroup.RoleOwner},
		detail: servergroup.Detail{Group: servergroup.Group{ID: groupID, Name: "Private group"}},
		invite: servergroup.Invite{Code: "ovt_synthetic", MaxUses: 2, ExpiresAt: time.Now().UTC().Add(time.Hour)},
	}
	handler, err := New(&fakeAuthService{}, &fakeUsageSyncService{}, &fakeAccountUsageService{}, groups, &fakeLeaderboardService{})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name       string
		method     string
		path       string
		body       string
		credential string
		wantStatus int
		wantBody   string
	}{
		{name: "create", method: http.MethodPost, path: "/v1/groups", body: `{"name":"Private group","timezone":"America/Vancouver"}`, credential: "device", wantStatus: http.StatusCreated, wantBody: "Private group"},
		{name: "list", method: http.MethodGet, path: "/v1/groups", credential: "device", wantStatus: http.StatusOK, wantBody: "groups"},
		{name: "read", method: http.MethodGet, path: "/v1/groups/" + groupID, credential: "device", wantStatus: http.StatusOK, wantBody: "Private group"},
		{name: "invite", method: http.MethodPost, path: "/v1/groups/" + groupID + "/invites", body: `{"expires_in_seconds":3600,"max_uses":2}`, credential: "device", wantStatus: http.StatusCreated, wantBody: "ovt_synthetic"},
		{name: "join", method: http.MethodPost, path: "/v1/group-invites/join", body: `{"code":"ovt_synthetic"}`, credential: "device", wantStatus: http.StatusOK, wantBody: "Private group"},
		{name: "leave", method: http.MethodDelete, path: "/v1/groups/" + groupID + "/membership", credential: "device", wantStatus: http.StatusNoContent},
		{name: "revoke", method: http.MethodDelete, path: "/v1/groups/" + groupID + "/members/" + userID, credential: "device", wantStatus: http.StatusNoContent},
		{name: "missing credential", method: http.MethodGet, path: "/v1/groups", wantStatus: http.StatusUnauthorized, wantBody: "unauthorized"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(test.method, test.path, strings.NewReader(test.body))
			if test.credential != "" {
				request.Header.Set("Authorization", "Bearer "+test.credential)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.wantStatus || !strings.Contains(response.Body.String(), test.wantBody) {
				t.Fatalf("response = %d %q", response.Code, response.Body.String())
			}
		})
	}
	groups.err = servergroup.ErrForbidden
	request := httptest.NewRequest(http.MethodPost, "/v1/groups/"+groupID+"/invites", strings.NewReader(`{"expires_in_seconds":3600,"max_uses":1}`))
	request.Header.Set("Authorization", "Bearer device")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden || strings.Contains(response.Body.String(), groupID) {
		t.Fatalf("forbidden response = %d %q", response.Code, response.Body.String())
	}
}

func TestGlobalBoardRoutes(t *testing.T) {
	account := &fakeAccountUsageService{}
	leaderboards := &fakeLeaderboardService{board: serverleaderboard.Board{Scope: serverleaderboard.ScopeGlobal, Timezone: "UTC"}}
	handler, err := New(&fakeAuthService{}, &fakeUsageSyncService{}, account, &fakeGroupService{}, leaderboards)
	if err != nil {
		t.Fatal(err)
	}
	send := func(method, path, body, credential string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(method, path, strings.NewReader(body))
		if credential != "" {
			request.Header.Set("Authorization", "Bearer "+credential)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	tests := []struct {
		name       string
		body       string
		credential string
		wantStatus int
		wantOptIn  *bool
	}{
		{name: "join", body: `{"participating":true}`, credential: "device", wantStatus: http.StatusOK, wantOptIn: boolPointer(true)},
		{name: "leave", body: `{"participating":false}`, credential: "device", wantStatus: http.StatusOK, wantOptIn: boolPointer(false)},
		// Nothing short of an explicit choice may opt an account in.
		{name: "empty object", body: `{}`, credential: "device", wantStatus: http.StatusBadRequest},
		{name: "null choice", body: `{"participating":null}`, credential: "device", wantStatus: http.StatusBadRequest},
		{name: "choice as text", body: `{"participating":"true"}`, credential: "device", wantStatus: http.StatusBadRequest},
		{name: "unknown field", body: `{"participating":true,"profile":"public"}`, credential: "device", wantStatus: http.StatusBadRequest},
		{name: "no body", body: ``, credential: "device", wantStatus: http.StatusBadRequest},
		{name: "no credential", body: `{"participating":true}`, wantStatus: http.StatusUnauthorized},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			account.optIn = nil
			response := send(http.MethodPut, "/v1/account/global-board", test.body, test.credential)
			if response.Code != test.wantStatus {
				t.Fatalf("status = %d %q, want %d", response.Code, response.Body.String(), test.wantStatus)
			}
			if (account.optIn == nil) != (test.wantOptIn == nil) || account.optIn != nil && *account.optIn != *test.wantOptIn {
				t.Fatalf("stored choice = %v, want %v", account.optIn, test.wantOptIn)
			}
		})
	}
	account.optInErr = serverauth.ErrUnauthorized
	if response := send(http.MethodPut, "/v1/account/global-board", `{"participating":true}`, "revoked"); response.Code != http.StatusUnauthorized {
		t.Fatalf("revoked credential status = %d", response.Code)
	}

	response := send(http.MethodGet, "/v1/global/leaderboard?period=week&metric=api_estimate", "", "device")
	if response.Code != http.StatusOK || !leaderboards.global || leaderboards.period != serverleaderboard.PeriodWeek ||
		leaderboards.metric != serverleaderboard.MetricAPIEstimate || !strings.Contains(response.Body.String(), `"scope":"global"`) {
		t.Fatalf("global board = %d %q (period %s, metric %s)", response.Code, response.Body.String(), leaderboards.period, leaderboards.metric)
	}
	if strings.Contains(response.Body.String(), "group_id") {
		t.Fatalf("global board names a group: %q", response.Body.String())
	}
	if response := send(http.MethodGet, "/v1/global/leaderboard", "", "device"); response.Code != http.StatusOK ||
		leaderboards.period != serverleaderboard.PeriodToday || leaderboards.metric != serverleaderboard.MetricTotalTokens {
		t.Fatalf("default global board = %d (period %s, metric %s)", response.Code, leaderboards.period, leaderboards.metric)
	}
	if response := send(http.MethodGet, "/v1/global/leaderboard", "", ""); response.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous global board status = %d, want 401: no public read yet", response.Code)
	}
}

func boolPointer(value bool) *bool { return &value }

func TestLeaderboardRouteUsesExplicitPeriodAndMetric(t *testing.T) {
	groupID := "10000000-0000-4000-8000-000000000001"
	leaderboards := &fakeLeaderboardService{board: serverleaderboard.Board{
		GroupID: groupID, GroupName: "Private group", Timezone: "America/Vancouver",
		Period: serverleaderboard.PeriodWeek, Metric: serverleaderboard.MetricAPIEstimate,
	}}
	handler, err := New(
		&fakeAuthService{}, &fakeUsageSyncService{}, &fakeAccountUsageService{},
		&fakeGroupService{}, leaderboards,
	)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/v1/groups/"+groupID+"/leaderboard?period=week&metric=api_estimate", nil)
	request.Header.Set("Authorization", "Bearer device")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || leaderboards.period != serverleaderboard.PeriodWeek || leaderboards.metric != serverleaderboard.MetricAPIEstimate || !strings.Contains(response.Body.String(), `"timezone":"America/Vancouver"`) {
		t.Fatalf("leaderboard response = %d %q/%#v", response.Code, response.Body.String(), leaderboards)
	}
}

func validSyncBatch() protocol.Batch {
	zero, output := uint64(0), uint64(7)
	bucket := protocol.Bucket{
		Start:  time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC),
		Source: "codex", Surface: "cli", ObservedModelID: "model-the-server-has-never-seen",
		ModelEvidence: "inferred", Measurement: "reported", NormalizationVersion: "1",
		Tokens: protocol.Tokens{
			InputUncached:     protocol.Value{Value: &zero, State: "complete"},
			InputCacheRead:    protocol.Value{State: "unavailable"},
			InputCacheWrite:   protocol.Value{State: "unavailable"},
			InputCacheWrite5m: protocol.Value{State: "unavailable"},
			InputCacheWrite1h: protocol.Value{State: "unavailable"},
			Output:            protocol.Value{Value: &output, State: "complete"},
			ReasoningOutput:   protocol.Value{State: "unavailable"},
		},
		Revision: 1,
	}
	parts := []string{
		bucket.Start.Format(time.RFC3339Nano), bucket.Source, bucket.Surface, bucket.ObservedModelID,
		bucket.ModelEvidence, bucket.Measurement, bucket.NormalizationVersion,
	}
	digest := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return protocol.Batch{SchemaVersion: protocol.SchemaVersion, Buckets: []protocol.Envelope{{
		BucketKey: hex.EncodeToString(digest[:]), Bucket: bucket,
	}}}
}

type fakeAuthService struct {
	start     serverauth.StartResponse
	startErr  error
	poll      serverauth.PollResponse
	refresh   serverauth.Session
	logoutErr error
}

func (f *fakeAuthService) Start(context.Context, string) (serverauth.StartResponse, error) {
	return f.start, f.startErr
}
func (f *fakeAuthService) Poll(context.Context, string) (serverauth.PollResponse, error) {
	return f.poll, nil
}
func (f *fakeAuthService) Refresh(context.Context, string) (serverauth.Session, error) {
	return f.refresh, nil
}
func (f *fakeAuthService) Logout(context.Context, string) error { return f.logoutErr }

type fakeUsageSyncService struct {
	credential string
	err        error
}

type fakeAccountUsageService struct {
	credential string
	usage      serveraccount.Usage
	deleted    bool
	deleteErr  error
	optIn      *bool
	optInErr   error
}

func (f *fakeAccountUsageService) SetGlobalOptIn(_ context.Context, credential string, participating bool) error {
	f.credential = credential
	if f.optInErr == nil {
		f.optIn = &participating
	}
	return f.optInErr
}

type fakeGroupService struct {
	group  servergroup.Group
	detail servergroup.Detail
	invite servergroup.Invite
	err    error
}

type fakeLeaderboardService struct {
	board  serverleaderboard.Board
	period serverleaderboard.Period
	metric serverleaderboard.Metric
	global bool
	err    error
}

func (f *fakeLeaderboardService) Global(_ context.Context, _ string, period serverleaderboard.Period, metric serverleaderboard.Metric) (serverleaderboard.Board, error) {
	f.period, f.metric, f.global = period, metric, true
	return f.board, f.err
}

func (f *fakeLeaderboardService) Get(_ context.Context, _, _ string, period serverleaderboard.Period, metric serverleaderboard.Metric) (serverleaderboard.Board, error) {
	f.period = period
	f.metric = metric
	return f.board, f.err
}

func (f *fakeGroupService) Create(context.Context, string, string, string) (servergroup.Group, error) {
	return f.group, f.err
}
func (f *fakeGroupService) List(context.Context, string) ([]servergroup.Group, error) {
	return []servergroup.Group{f.group}, f.err
}
func (f *fakeGroupService) Get(context.Context, string, string) (servergroup.Detail, error) {
	return f.detail, f.err
}
func (f *fakeGroupService) Invite(context.Context, string, string, time.Duration, int) (servergroup.Invite, error) {
	return f.invite, f.err
}
func (f *fakeGroupService) Join(context.Context, string, string) (servergroup.Group, error) {
	return f.group, f.err
}
func (f *fakeGroupService) Leave(context.Context, string, string) error { return f.err }
func (f *fakeGroupService) Revoke(context.Context, string, string, string) error {
	return f.err
}

func (f *fakeAccountUsageService) Usage(_ context.Context, credential string) (serveraccount.Usage, error) {
	f.credential = credential
	return f.usage, nil
}

func (f *fakeAccountUsageService) Delete(_ context.Context, credential string) error {
	f.credential = credential
	f.deleted = true
	return f.deleteErr
}

func (f *fakeUsageSyncService) Apply(_ context.Context, credential string, batch protocol.Batch) (protocol.BatchResponse, error) {
	f.credential = credential
	if f.err != nil {
		return protocol.BatchResponse{}, f.err
	}
	acknowledgements := make([]protocol.Acknowledgement, 0, len(batch.Buckets))
	for _, bucket := range batch.Buckets {
		acknowledgements = append(acknowledgements, protocol.Acknowledgement{
			BucketKey: bucket.BucketKey, Revision: bucket.Bucket.Revision, Status: protocol.AckApplied,
		})
	}
	return protocol.BatchResponse{Acknowledgements: acknowledgements}, nil
}
