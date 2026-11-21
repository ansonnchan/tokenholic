package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
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
	"github.com/ansonnchan/tokenholic/server/httpapi"
	"github.com/ansonnchan/tokenholic/server/leaderboard"
)

// credentialAuthenticator maps a bearer credential to its device.
type credentialAuthenticator map[string]serverauth.Principal

func (a credentialAuthenticator) Authenticate(_ context.Context, credential string) (serverauth.Principal, error) {
	principal, found := a[credential]
	if !found {
		return serverauth.Principal{}, serverauth.ErrUnauthorized
	}
	return principal, nil
}

type unusedAuthService struct{}

func (unusedAuthService) Start(context.Context, string) (serverauth.StartResponse, error) {
	return serverauth.StartResponse{}, errors.New("unused")
}
func (unusedAuthService) Poll(context.Context, string) (serverauth.PollResponse, error) {
	return serverauth.PollResponse{}, errors.New("unused")
}
func (unusedAuthService) Refresh(context.Context, string) (serverauth.Session, error) {
	return serverauth.Session{}, errors.New("unused")
}
func (unusedAuthService) Logout(context.Context, string) error { return errors.New("unused") }

type unusedSyncService struct{}

func (unusedSyncService) Apply(context.Context, string, protocol.Batch) (protocol.BatchResponse, error) {
	return protocol.BatchResponse{}, errors.New("unused")
}

// The whole HTTP API runs over real storage. Olive owns a group, Mia is a
// member, Otto is signed in but belongs to nothing, and Pat never makes
// anything public.
func TestPostgresProfilesPublicVisibilityAndGroupLifecycle(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	authRepository, err := NewAuthRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	authenticator := credentialAuthenticator{}
	users := map[string]serverauth.Principal{}
	register := func(index int, login, avatar string) {
		t.Helper()
		id := func(kind int) string { return fmt.Sprintf("63000000-0000-4000-8000-%04d%08d", kind, index) }
		identity, err := authRepository.RegisterDevice(ctx, serverauth.GitHubUser{ID: int64(9300 + index), Login: login, AvatarURL: avatar}, serverauth.DeviceRegistration{
			UserID: id(1), DeviceID: id(2), ClientVersion: "test",
			Credential: serverauth.NewCredential{CredentialID: id(3), SecretHash: sha256.Sum256([]byte(login)), ExpiresAt: time.Now().UTC().Add(time.Hour)},
		})
		if err != nil {
			t.Fatal(err)
		}
		users[login] = serverauth.Principal{UserID: identity.UserID, DeviceID: identity.DeviceID}
		authenticator[strings.ToLower(login)+"-credential"] = users[login]
	}
	register(1, "Olive", "https://avatars.githubusercontent.com/u/9301?v=4")
	register(2, "mia", "https://avatars.githubusercontent.com/u/9302?v=4")
	register(3, "otto", "")
	// An avatar anywhere but GitHub's avatar host is dropped, not stored, and
	// does not fail the login.
	register(4, "pat", "https://images.example.test/pat.png")

	accountRepository, err := NewAccountRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	accounts, err := serveraccount.NewService(authenticator, accountRepository)
	if err != nil {
		t.Fatal(err)
	}
	groupRepository, err := NewGroupRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	groups, err := servergroup.NewService(authenticator, groupRepository)
	if err != nil {
		t.Fatal(err)
	}
	if err := groups.SetWebOrigin("https://tokenholic.example"); err != nil {
		t.Fatal(err)
	}
	leaderboardRepository, err := NewLeaderboardRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	boards, err := leaderboard.NewService(authenticator, leaderboardRepository)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := httpapi.New(unusedAuthService{}, unusedSyncService{}, accounts, groups, boards)
	if err != nil {
		t.Fatal(err)
	}
	send := func(method, path, credential, body string) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(method, path, strings.NewReader(body))
		if credential != "" {
			request.Header.Set("Authorization", "Bearer "+credential)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	expect := func(response *httptest.ResponseRecorder, status int, what string) *httptest.ResponseRecorder {
		t.Helper()
		if response.Code != status {
			t.Fatalf("%s = %d %q, want %d", what, response.Code, response.Body.String(), status)
		}
		return response
	}
	decode := func(response *httptest.ResponseRecorder, target any) {
		t.Helper()
		if err := json.Unmarshal(response.Body.Bytes(), target); err != nil {
			t.Fatalf("decode %q: %v", response.Body.String(), err)
		}
	}
	// sameMiss requires a response to be indistinguishable from the answer
	// for a record that never existed: status, every header, and body.
	sameMiss := func(reference *httptest.ResponseRecorder, path, why string) {
		t.Helper()
		response := send(http.MethodGet, path, "", "")
		if response.Code != http.StatusNotFound || response.Code != reference.Code || response.Body.String() != reference.Body.String() ||
			!reflect.DeepEqual(response.Header(), reference.Header()) {
			t.Fatalf("%s: GET %s = %d %q %v, want exactly the answer for a nonexistent record (%d %q %v)",
				why, path, response.Code, response.Body.String(), response.Header(), reference.Code, reference.Body.String(), reference.Header())
		}
	}

	// ---- Profiles ----
	noSuchUser := expect(send(http.MethodGet, "/v1/public/users/nobody-here", "", ""), http.StatusNotFound, "nonexistent profile")
	sameMiss(noSuchUser, "/v1/public/users/Olive", "a profile is private by default")
	var own serveraccount.Profile
	decode(expect(send(http.MethodGet, "/v1/account/profile", "olive-credential", ""), http.StatusOK, "own profile"), &own)
	if own.Login != "Olive" || own.Visibility != serveraccount.VisibilityPrivate || own.AvatarURL != "https://avatars.githubusercontent.com/u/9301?v=4" || own.GlobalOptIn {
		t.Fatalf("own profile = %#v", own)
	}
	var pat serveraccount.Profile
	decode(expect(send(http.MethodGet, "/v1/account/profile", "pat-credential", ""), http.StatusOK, "Pat's profile"), &pat)
	if pat.Login != "pat" || pat.AvatarURL != "" {
		t.Fatalf("Pat's profile = %#v; an avatar from an unexpected host must not be stored", pat)
	}
	expect(send(http.MethodPut, "/v1/account/profile", "olive-credential", `{"visibility":"everyone"}`), http.StatusBadRequest, "invalid visibility")
	sameMiss(noSuchUser, "/v1/public/users/olive", "a refused change published nothing")
	expect(send(http.MethodPut, "/v1/account/profile", "olive-credential", `{"visibility":"public"}`), http.StatusOK, "publish profile")
	for _, login := range []string{"Olive", "olive", "OLIVE"} {
		var public serveraccount.PublicProfile
		response := expect(send(http.MethodGet, "/v1/public/users/"+login, "", ""), http.StatusOK, "public profile")
		decode(response, &public)
		if public.Login != "Olive" || public.AvatarURL == "" || public.AllTime.TotalTokens.State != serveraccount.AvailabilityEmpty {
			t.Fatalf("public profile = %#v", public)
		}
		if strings.Contains(response.Body.String(), users["Olive"].UserID) || strings.Contains(response.Body.String(), users["Olive"].DeviceID) {
			t.Fatalf("public profile exposes an internal identifier: %s", response.Body.String())
		}
	}
	sameMiss(noSuchUser, "/v1/public/users/pat", "another account stays private")
	sameMiss(noSuchUser, "/v1/public/users/mia", "another account stays private")
	expect(send(http.MethodPut, "/v1/account/profile", "olive-credential", `{"visibility":"private"}`), http.StatusOK, "unpublish profile")
	sameMiss(noSuchUser, "/v1/public/users/olive", "a profile made private again")
	expect(send(http.MethodPut, "/v1/account/profile", "olive-credential", `{"visibility":"public"}`), http.StatusOK, "publish profile again")

	// ---- A private group ----
	var group servergroup.Group
	decode(expect(send(http.MethodPost, "/v1/groups", "olive-credential", `{"name":"Night shift","timezone":"UTC"}`), http.StatusCreated, "create group"), &group)
	if group.Visibility != servergroup.VisibilityPrivate || group.PublicSlug != "" {
		t.Fatalf("new group = %#v, want private without a slug", group)
	}
	groupPath := "/v1/groups/" + group.ID
	var invite servergroup.Invite
	decode(expect(send(http.MethodPost, groupPath+"/invites", "olive-credential", `{"expires_in_seconds":3600,"max_uses":1}`), http.StatusCreated, "create invite"), &invite)
	if invite.Link != "https://tokenholic.example/invite/#"+invite.Code {
		t.Fatalf("invite link = %q for code %q", invite.Link, invite.Code)
	}
	noSuchInvite := expect(send(http.MethodGet, "/v1/public/invites/ovt_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "", ""), http.StatusNotFound, "nonexistent invite")
	var preview servergroup.InvitePreview
	decode(expect(send(http.MethodGet, "/v1/public/invites/"+invite.Code, "", ""), http.StatusOK, "invite preview"), &preview)
	if preview.GroupName != "Night shift" || !preview.ExpiresAt.Equal(invite.ExpiresAt) {
		t.Fatalf("invite preview = %#v", preview)
	}
	expect(send(http.MethodPost, "/v1/group-invites/join", "mia-credential", `{"code":"`+invite.Code+`"}`), http.StatusOK, "join group")
	sameMiss(noSuchInvite, "/v1/public/invites/"+invite.Code, "a used-up invite")
	sameMiss(noSuchInvite, "/v1/public/invites/not-a-code", "a malformed invite code")

	// ---- Owner-only operations ----
	// A member is told the operation is forbidden. Someone outside the group
	// is told exactly what they would be told about a group that does not
	// exist.
	const missingGroup = "/v1/groups/63000000-0000-4000-8000-999999999999"
	ownerOnly := []struct{ name, method, suffix, body string }{
		{"set visibility", http.MethodPut, "/visibility", `{"visibility":"public"}`},
		{"transfer ownership", http.MethodPut, "/owner", `{"login":"mia"}`},
		{"delete", http.MethodDelete, "", `{"confirmation":"DELETE"}`},
		{"create invite", http.MethodPost, "/invites", `{"expires_in_seconds":3600,"max_uses":1}`},
	}
	for _, operation := range ownerOnly {
		expect(send(operation.method, groupPath+operation.suffix, "mia-credential", operation.body), http.StatusForbidden, "member: "+operation.name)
		outside := expect(send(operation.method, groupPath+operation.suffix, "otto-credential", operation.body), http.StatusNotFound, "outsider: "+operation.name)
		missing := send(operation.method, missingGroup+operation.suffix, "otto-credential", operation.body)
		if missing.Code != outside.Code || missing.Body.String() != outside.Body.String() {
			t.Fatalf("%s: an outsider can tell a real group (%d %q) from a missing one (%d %q)", operation.name, outside.Code, outside.Body.String(), missing.Code, missing.Body.String())
		}
		expect(send(operation.method, groupPath+operation.suffix, "", operation.body), http.StatusUnauthorized, "anonymous: "+operation.name)
	}
	var detail servergroup.Detail
	decode(expect(send(http.MethodGet, groupPath, "olive-credential", ""), http.StatusOK, "group after refused operations"), &detail)
	if detail.Group.Visibility != servergroup.VisibilityPrivate || len(detail.Members) != 2 || detail.Members[0].DisplayName != "Olive" || detail.Members[0].Role != servergroup.RoleOwner {
		t.Fatalf("group changed by a refused operation: %#v", detail)
	}

	// ---- A public group ----
	noSuchBoard := expect(send(http.MethodGet, "/v1/public/groups/g-aaaaaaaa/leaderboard", "", ""), http.StatusNotFound, "nonexistent public board")
	decode(expect(send(http.MethodPut, groupPath+"/visibility", "olive-credential", `{"visibility":"public"}`), http.StatusOK, "publish group"), &group)
	if group.Visibility != servergroup.VisibilityPublic || len(group.PublicSlug) != 10 {
		t.Fatalf("published group = %#v", group)
	}
	boardPath := "/v1/public/groups/" + group.PublicSlug + "/leaderboard"
	readBoard := func() leaderboard.Board {
		t.Helper()
		var board leaderboard.Board
		response := expect(send(http.MethodGet, boardPath+"?period=all_time", "", ""), http.StatusOK, "public board")
		decode(response, &board)
		for _, principal := range users {
			if strings.Contains(response.Body.String(), principal.UserID) || strings.Contains(response.Body.String(), group.ID) {
				t.Fatalf("public board exposes an internal identifier: %s", response.Body.String())
			}
		}
		return board
	}
	// Mia's profile is private, so the public board does not show that she
	// is a member at all.
	board := readBoard()
	if board.Scope != leaderboard.ScopePublicGroup || board.GroupName != "Night shift" || board.PublicSlug != group.PublicSlug || board.GroupID != "" ||
		board.TotalMembers != 1 || len(board.Rows) != 1 || board.Rows[0].DisplayName != "Olive" || board.Rows[0].AvatarURL == "" || !board.RanksHidden {
		t.Fatalf("public board = %#v", board)
	}
	expect(send(http.MethodPut, "/v1/account/profile", "mia-credential", `{"visibility":"public"}`), http.StatusOK, "Mia publishes her profile")
	if board = readBoard(); board.TotalMembers != 2 || len(board.Rows) != 2 {
		t.Fatalf("public board after a member published = %#v", board)
	}
	// The members' own view is unchanged and still needs membership.
	expect(send(http.MethodGet, groupPath+"/leaderboard", "otto-credential", ""), http.StatusNotFound, "outsider reading the member board of a public group")
	// Private again: the same slug answers like one that never existed.
	decode(expect(send(http.MethodPut, groupPath+"/visibility", "olive-credential", `{"visibility":"private"}`), http.StatusOK, "unpublish group"), &group)
	sameMiss(noSuchBoard, boardPath, "a group made private again")
	slug := group.PublicSlug
	decode(expect(send(http.MethodPut, groupPath+"/visibility", "olive-credential", `{"visibility":"public"}`), http.StatusOK, "publish group again"), &group)
	if group.PublicSlug != slug {
		t.Fatalf("slug changed from %q to %q; a published link must survive going private and back", slug, group.PublicSlug)
	}
	readBoard()

	// ---- Ownership transfer ----
	expect(send(http.MethodPut, groupPath+"/owner", "olive-credential", `{"login":"otto"}`), http.StatusConflict, "transfer to a non-member")
	expect(send(http.MethodPut, groupPath+"/owner", "olive-credential", `{"login":"nobody-here"}`), http.StatusConflict, "transfer to an unknown login")
	expect(send(http.MethodPut, groupPath+"/owner", "olive-credential", `{"login":"MIA"}`), http.StatusNoContent, "transfer to a member")
	decode(expect(send(http.MethodGet, groupPath, "mia-credential", ""), http.StatusOK, "group after transfer"), &detail)
	if detail.Group.Role != servergroup.RoleOwner || len(detail.Members) != 2 || detail.Members[0].DisplayName != "mia" || detail.Members[1].Role != servergroup.RoleMember {
		t.Fatalf("group after transfer = %#v", detail)
	}
	var owners int
	var ownerID string
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM group_memberships WHERE group_id = $1::uuid AND role = 'owner'`, group.ID).Scan(&owners); err != nil || owners != 1 {
		t.Fatalf("owners after transfer = %d, %v", owners, err)
	}
	if err := pool.QueryRow(ctx, `SELECT owner_user_id::text FROM groups WHERE id = $1::uuid`, group.ID).Scan(&ownerID); err != nil || ownerID != users["mia"].UserID {
		t.Fatalf("recorded owner = %q, %v", ownerID, err)
	}
	// The previous owner is now an ordinary member.
	expect(send(http.MethodPut, groupPath+"/visibility", "olive-credential", `{"visibility":"private"}`), http.StatusForbidden, "previous owner setting visibility")
	expect(send(http.MethodDelete, groupPath, "olive-credential", `{"confirmation":"DELETE"}`), http.StatusForbidden, "previous owner deleting")
	expect(send(http.MethodDelete, groupPath+"/membership", "olive-credential", ""), http.StatusNoContent, "previous owner leaving")

	// ---- Deletion ----
	expect(send(http.MethodDelete, groupPath, "mia-credential", `{}`), http.StatusBadRequest, "delete without confirmation")
	expect(send(http.MethodGet, boardPath, "", ""), http.StatusOK, "public board before deletion")
	expect(send(http.MethodDelete, groupPath, "mia-credential", `{"confirmation":"DELETE"}`), http.StatusNoContent, "delete group")
	sameMiss(noSuchBoard, boardPath, "a deleted group")
	expect(send(http.MethodGet, groupPath, "mia-credential", ""), http.StatusNotFound, "deleted group read by its former owner")
	for table, want := range map[string]int{"groups": 0, "group_memberships": 0, "group_invites": 0, "users": 4} {
		var got int
		if err := pool.QueryRow(ctx, "SELECT COUNT(*) FROM "+table).Scan(&got); err != nil || got != want {
			t.Fatalf("%s rows after deleting the group = %d, %v; want %d", table, got, err, want)
		}
	}

	// A deleted account's public profile is gone like any other miss.
	expect(send(http.MethodGet, "/v1/public/users/mia", "", ""), http.StatusOK, "Mia's public profile")
	if err := accountRepository.DeleteAccount(ctx, users["mia"]); err != nil {
		t.Fatal(err)
	}
	sameMiss(noSuchUser, "/v1/public/users/mia", "a deleted account")
}
