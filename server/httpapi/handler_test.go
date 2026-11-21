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
	"reflect"
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

// Every public route answers a miss with the same bytes, whatever the reason
// and whichever kind of record was asked for, and takes no credential.
func TestPublicRoutesAnswerEveryMissIdentically(t *testing.T) {
	handler, err := New(&fakeAuthService{}, &fakeUsageSyncService{}, &fakeAccountUsageService{}, &fakeGroupService{}, &fakeLeaderboardService{})
	if err != nil {
		t.Fatal(err)
	}
	get := func(path string) *httptest.ResponseRecorder {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		return response
	}
	for path, want := range map[string]string{
		"/v1/public/users/public-member":           `"login":"public-member"`,
		"/v1/public/groups/g-publicab/leaderboard": `"scope":"public_group"`,
		"/v1/public/invites/ovt_valid":             `"group_name":"Private group"`,
	} {
		if response := get(path); response.Code != http.StatusOK || !strings.Contains(response.Body.String(), want) {
			t.Fatalf("GET %s = %d %q", path, response.Code, response.Body.String())
		}
	}
	misses := []string{
		"/v1/public/users/private-member", "/v1/public/users/nobody",
		"/v1/public/groups/g-privatea/leaderboard", "/v1/public/groups/g-nothere/leaderboard",
		"/v1/public/invites/ovt_expired", "/v1/public/invites/unknown",
	}
	reference := get(misses[0])
	if reference.Code != http.StatusNotFound || reference.Body.String() != "{\"error\":\"not_found\"}\n" {
		t.Fatalf("miss = %d %q", reference.Code, reference.Body.String())
	}
	for _, path := range misses[1:] {
		response := get(path)
		if response.Code != reference.Code || response.Body.String() != reference.Body.String() || !reflect.DeepEqual(response.Header(), reference.Header()) {
			t.Fatalf("GET %s = %d %q %v, want exactly the reference miss", path, response.Code, response.Body.String(), response.Header())
		}
	}
	// A bad period is refused the same way for a public and a private slug.
	public, private := get("/v1/public/groups/g-publicab/leaderboard?period=future"), get("/v1/public/groups/g-privatea/leaderboard?period=future")
	if public.Code != http.StatusBadRequest || private.Code != public.Code || private.Body.String() != public.Body.String() {
		t.Fatalf("invalid period = %d/%d", public.Code, private.Code)
	}
	// Public routes are read-only.
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(method, "/v1/public/users/public-member", strings.NewReader("{}")))
		if response.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s on a public route = %d, want 405", method, response.Code)
		}
	}
}

func TestProfileAndGroupLifecycleRoutes(t *testing.T) {
	account, groups := &fakeAccountUsageService{}, &fakeGroupService{group: servergroup.Group{ID: "10000000-0000-4000-8000-000000000001", Name: "Private group"}}
	handler, err := New(&fakeAuthService{}, &fakeUsageSyncService{}, account, groups, &fakeLeaderboardService{})
	if err != nil {
		t.Fatal(err)
	}
	const group = "/v1/groups/10000000-0000-4000-8000-000000000001"
	tests := []struct {
		name, method, path, body, credential string
		wantStatus                           int
		wantBody                             string
	}{
		{name: "read own profile", method: http.MethodGet, path: "/v1/account/profile", credential: "device", wantStatus: http.StatusOK, wantBody: `"visibility":"private"`},
		{name: "profile needs a credential", method: http.MethodGet, path: "/v1/account/profile", wantStatus: http.StatusUnauthorized},
		{name: "publish profile", method: http.MethodPut, path: "/v1/account/profile", body: `{"visibility":"public"}`, credential: "device", wantStatus: http.StatusOK, wantBody: `"visibility":"public"`},
		{name: "invalid profile visibility", method: http.MethodPut, path: "/v1/account/profile", body: `{"visibility":"unlisted"}`, credential: "device", wantStatus: http.StatusBadRequest},
		{name: "profile visibility omitted", method: http.MethodPut, path: "/v1/account/profile", body: `{}`, credential: "device", wantStatus: http.StatusBadRequest},
		{name: "profile change needs a credential", method: http.MethodPut, path: "/v1/account/profile", body: `{"visibility":"public"}`, wantStatus: http.StatusUnauthorized},
		{name: "publish group", method: http.MethodPut, path: group + "/visibility", body: `{"visibility":"public"}`, credential: "device", wantStatus: http.StatusOK, wantBody: `"visibility":"public"`},
		{name: "group visibility needs a credential", method: http.MethodPut, path: group + "/visibility", body: `{"visibility":"public"}`, wantStatus: http.StatusUnauthorized},
		{name: "transfer ownership", method: http.MethodPut, path: group + "/owner", body: `{"login":"new-owner"}`, credential: "device", wantStatus: http.StatusNoContent},
		{name: "transfer needs a credential", method: http.MethodPut, path: group + "/owner", body: `{"login":"new-owner"}`, wantStatus: http.StatusUnauthorized},
		{name: "delete without confirmation", method: http.MethodDelete, path: group, body: `{}`, credential: "device", wantStatus: http.StatusBadRequest, wantBody: "confirmation_required"},
		{name: "delete needs a credential", method: http.MethodDelete, path: group, body: `{"confirmation":"DELETE"}`, wantStatus: http.StatusUnauthorized},
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
				t.Fatalf("%s %s = %d %q, want %d containing %q", test.method, test.path, response.Code, response.Body.String(), test.wantStatus, test.wantBody)
			}
		})
	}
	if account.visibility != serveraccount.VisibilityPublic || groups.transferred != "new-owner" || groups.deleted {
		t.Fatalf("state after the table = visibility %q, transfer %q, deleted %t", account.visibility, groups.transferred, groups.deleted)
	}
	request := httptest.NewRequest(http.MethodDelete, group, strings.NewReader(`{"confirmation":"DELETE"}`))
	request.Header.Set("Authorization", "Bearer device")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent || !groups.deleted {
		t.Fatalf("confirmed delete = %d, deleted %t", response.Code, groups.deleted)
	}
	// Owner-only failures keep their distinct, existing meanings for members.
	for wantStatus, serviceErr := range map[int]error{
		http.StatusForbidden: servergroup.ErrForbidden, http.StatusNotFound: servergroup.ErrNotFound, http.StatusConflict: servergroup.ErrNotMember,
	} {
		groups.err = serviceErr
		request := httptest.NewRequest(http.MethodPut, group+"/owner", strings.NewReader(`{"login":"new-owner"}`))
		request.Header.Set("Authorization", "Bearer device")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != wantStatus {
			t.Fatalf("transfer with %v = %d, want %d", serviceErr, response.Code, wantStatus)
		}
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
	visibility serveraccount.Visibility
}

func (f *fakeAccountUsageService) Profile(context.Context, string) (serveraccount.Profile, error) {
	return serveraccount.Profile{Login: "member", Visibility: serveraccount.VisibilityPrivate}, nil
}

func (f *fakeAccountUsageService) SetProfileVisibility(_ context.Context, _ string, visibility serveraccount.Visibility) error {
	if visibility != serveraccount.VisibilityPublic && visibility != serveraccount.VisibilityPrivate {
		return serveraccount.ErrInvalidVisibility
	}
	f.visibility = visibility
	return nil
}

func (f *fakeAccountUsageService) PublicProfile(_ context.Context, login string) (serveraccount.PublicProfile, error) {
	if login != "public-member" {
		return serveraccount.PublicProfile{}, serveraccount.ErrProfileNotFound
	}
	return serveraccount.PublicProfile{Login: login}, nil
}

func (f *fakeAccountUsageService) SetGlobalOptIn(_ context.Context, credential string, participating bool) error {
	f.credential = credential
	if f.optInErr == nil {
		f.optIn = &participating
	}
	return f.optInErr
}

type fakeGroupService struct {
	group       servergroup.Group
	detail      servergroup.Detail
	invite      servergroup.Invite
	err         error
	deleted     bool
	transferred string
}

type fakeLeaderboardService struct {
	board  serverleaderboard.Board
	period serverleaderboard.Period
	metric serverleaderboard.Metric
	global bool
	err    error
}

func (f *fakeLeaderboardService) PublicGroup(_ context.Context, slug string, period serverleaderboard.Period, _ serverleaderboard.Metric) (serverleaderboard.Board, error) {
	if !period.Valid() {
		return serverleaderboard.Board{}, servergroup.ErrInvalidInput
	}
	if slug != "g-publicab" {
		return serverleaderboard.Board{}, servergroup.ErrNotFound
	}
	return serverleaderboard.Board{Scope: serverleaderboard.ScopePublicGroup, PublicSlug: slug}, nil
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
func (f *fakeGroupService) SetVisibility(_ context.Context, _, _ string, visibility servergroup.Visibility) (servergroup.Group, error) {
	group := f.group
	group.Visibility = visibility
	return group, f.err
}
func (f *fakeGroupService) Delete(context.Context, string, string) error {
	f.deleted = true
	return f.err
}
func (f *fakeGroupService) Transfer(_ context.Context, _, _, login string) error {
	f.transferred = login
	return f.err
}
func (f *fakeGroupService) InvitePreview(_ context.Context, code string) (servergroup.InvitePreview, error) {
	if code != "ovt_valid" {
		return servergroup.InvitePreview{}, servergroup.ErrInvalidInvite
	}
	return servergroup.InvitePreview{GroupName: "Private group"}, nil
}
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
