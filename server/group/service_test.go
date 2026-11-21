package group

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	serverauth "github.com/ansonnchan/tokenholic/server/auth"
)

func TestServiceCreatesValidatedGroup(t *testing.T) {
	repository := &fakeRepository{}
	service, err := NewService(fakeAuthenticator{}, repository)
	if err != nil {
		t.Fatal(err)
	}
	created, err := service.Create(t.Context(), "credential", "  Quiet Team  ", "America/Vancouver")
	if err != nil {
		t.Fatal(err)
	}
	if repository.created.Name != "Quiet Team" || repository.created.Timezone != "America/Vancouver" || repository.created.ID == "" || created.Role != RoleOwner {
		t.Fatalf("created group = %#v, input = %#v", created, repository.created)
	}
	for _, test := range []struct {
		name     string
		group    string
		timezone string
	}{
		{name: "blank name", group: " ", timezone: "UTC"},
		{name: "multiline name", group: "Team\nHidden", timezone: "UTC"},
		{name: "unknown timezone", group: "Team", timezone: "Mars/Olympus"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := service.Create(t.Context(), "credential", test.group, test.timezone); !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("Create() error = %v", err)
			}
		})
	}
}

func TestServiceCreatesHashedLimitedInvite(t *testing.T) {
	repository := &fakeRepository{}
	service, err := NewService(fakeAuthenticator{}, repository)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }
	invite, err := service.Invite(t.Context(), "credential", testGroupID, time.Hour, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(invite.Code) != 36 || repository.invite.CodeHash == [32]byte{} || invite.ExpiresAt != now.Add(time.Hour) || repository.invite.MaxUses != 2 {
		t.Fatalf("invite = %#v, stored = %#v", invite, repository.invite)
	}
	if _, err := service.Invite(t.Context(), "credential", testGroupID, time.Minute, 1); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("short invite error = %v", err)
	}
	if _, err := service.Invite(t.Context(), "credential", testGroupID, time.Hour, 101); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("large invite error = %v", err)
	}
}

func TestServiceAuthenticatesEveryOperation(t *testing.T) {
	authenticator := fakeAuthenticator{err: serverauth.ErrUnauthorized}
	service, err := NewService(authenticator, &fakeRepository{})
	if err != nil {
		t.Fatal(err)
	}
	operations := []func() error{
		func() error { _, err := service.Create(t.Context(), "bad", "Team", "UTC"); return err },
		func() error { _, err := service.List(t.Context(), "bad"); return err },
		func() error { _, err := service.Get(t.Context(), "bad", testGroupID); return err },
		func() error { _, err := service.Invite(t.Context(), "bad", testGroupID, time.Hour, 1); return err },
		func() error {
			_, err := service.Join(t.Context(), "bad", "ovt_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
			return err
		},
		func() error { return service.Leave(t.Context(), "bad", testGroupID) },
		func() error { return service.Revoke(t.Context(), "bad", testGroupID, testUserID) },
		func() error {
			_, err := service.SetVisibility(t.Context(), "bad", testGroupID, VisibilityPublic)
			return err
		},
		func() error { return service.Delete(t.Context(), "bad", testGroupID) },
		func() error { return service.Transfer(t.Context(), "bad", testGroupID, "member") },
	}
	for index, operation := range operations {
		if err := operation(); !errors.Is(err, serverauth.ErrUnauthorized) {
			t.Fatalf("operation %d error = %v", index, err)
		}
	}
}

func TestServiceValidatesGroupLifecycleInput(t *testing.T) {
	repository := &fakeRepository{}
	service, err := NewService(fakeAuthenticator{}, repository)
	if err != nil {
		t.Fatal(err)
	}
	slugs := map[string]bool{}
	for attempt := 0; attempt < 3; attempt++ {
		group, err := service.SetVisibility(t.Context(), "credential", testGroupID, VisibilityPublic)
		if err != nil || group.Visibility != VisibilityPublic || len(repository.slug) != 10 || !strings.HasPrefix(repository.slug, "g-") || strings.ToLower(repository.slug) != repository.slug {
			t.Fatalf("SetVisibility() = %#v, %v with slug %q", group, err, repository.slug)
		}
		slugs[repository.slug] = true
	}
	if len(slugs) != 3 {
		t.Fatalf("candidate slugs = %v, want a fresh random one each time", slugs)
	}
	if _, err := service.SetVisibility(t.Context(), "credential", testGroupID, "unlisted"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("invalid visibility error = %v", err)
	}
	if _, err := service.SetVisibility(t.Context(), "credential", "bad", VisibilityPublic); !errors.Is(err, ErrNotFound) {
		t.Fatalf("invalid group error = %v", err)
	}
	if err := service.Delete(t.Context(), "credential", "bad"); !errors.Is(err, ErrNotFound) || repository.deleted != "" {
		t.Fatalf("Delete(bad) = %v, deleted %q", err, repository.deleted)
	}
	if err := service.Delete(t.Context(), "credential", testGroupID); err != nil || repository.deleted != testGroupID {
		t.Fatalf("Delete() = %v, deleted %q", err, repository.deleted)
	}
	if err := service.Transfer(t.Context(), "credential", testGroupID, "  New-Owner "); err != nil || repository.transferred != "New-Owner" {
		t.Fatalf("Transfer() = %v, target %q", err, repository.transferred)
	}
	for _, login := range []string{"", "   ", strings.Repeat("a", 40)} {
		if err := service.Transfer(t.Context(), "credential", testGroupID, login); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("Transfer(%q) error = %v", login, err)
		}
	}
}

// A clock with nanosecond precision must not produce an expiry that storage,
// which keeps microseconds, would report differently later.
func TestServiceInviteExpiryMatchesStoragePrecision(t *testing.T) {
	repository := &fakeRepository{}
	service, err := NewService(fakeAuthenticator{}, repository)
	if err != nil {
		t.Fatal(err)
	}
	service.now = func() time.Time { return time.Date(2026, 11, 21, 20, 40, 24, 606640123, time.UTC) }
	invite, err := service.Invite(t.Context(), "credential", testGroupID, time.Hour, 1)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 11, 21, 21, 40, 24, 606640000, time.UTC)
	if !invite.ExpiresAt.Equal(want) || !repository.invite.ExpiresAt.Equal(want) {
		t.Fatalf("expiry returned %s, stored %s; want both %s", invite.ExpiresAt, repository.invite.ExpiresAt, want)
	}
}

func TestServiceInviteLinksAndPreview(t *testing.T) {
	repository := &fakeRepository{}
	// A preview needs no credential.
	service, err := NewService(fakeAuthenticator{err: serverauth.ErrUnauthorized}, repository)
	if err != nil {
		t.Fatal(err)
	}
	if preview, err := service.InvitePreview(t.Context(), "ovt_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"); err != nil || preview.GroupName != "Team" {
		t.Fatalf("InvitePreview() = %#v, %v", preview, err)
	}
	for _, code := range []string{"", "ovt_short", "xyz_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", strings.Repeat("a", 500)} {
		if _, err := service.InvitePreview(t.Context(), code); !errors.Is(err, ErrInvalidInvite) {
			t.Fatalf("InvitePreview(%q) error = %v", code, err)
		}
	}
	if repository.previewed != 1 {
		t.Fatalf("%d previews reached storage, want only the well-formed code", repository.previewed)
	}

	signedIn, err := NewService(fakeAuthenticator{}, repository)
	if err != nil {
		t.Fatal(err)
	}
	invite, err := signedIn.Invite(t.Context(), "credential", testGroupID, time.Hour, 1)
	if err != nil || invite.Link != "" {
		t.Fatalf("invite without a website origin = %#v, %v; want no link", invite, err)
	}
	for _, origin := range []string{"http://tokenholic.example", "https://tokenholic.example/app", "https://user@tokenholic.example", "https://tokenholic.example?x=1", "tokenholic.example", ""} {
		if err := signedIn.SetWebOrigin(origin); err == nil {
			t.Fatalf("SetWebOrigin(%q) error = nil", origin)
		}
	}
	if err := signedIn.SetWebOrigin("https://tokenholic.example/"); err != nil {
		t.Fatal(err)
	}
	invite, err = signedIn.Invite(t.Context(), "credential", testGroupID, time.Hour, 1)
	if err != nil || invite.Link != "https://tokenholic.example/invite/#"+invite.Code {
		t.Fatalf("invite link = %q for code %q, %v", invite.Link, invite.Code, err)
	}
}

const (
	testGroupID = "10000000-0000-4000-8000-000000000001"
	testUserID  = "10000000-0000-4000-8000-000000000002"
)

type fakeAuthenticator struct {
	err error
}

func (f fakeAuthenticator) Authenticate(context.Context, string) (serverauth.Principal, error) {
	return serverauth.Principal{UserID: testUserID, DeviceID: "10000000-0000-4000-8000-000000000003"}, f.err
}

type fakeRepository struct {
	created     NewGroup
	invite      NewInvite
	visibility  Visibility
	slug        string
	deleted     string
	transferred string
	previewed   int
}

func (f *fakeRepository) SetVisibility(_ context.Context, _ serverauth.Principal, groupID string, visibility Visibility, slug string) (Group, error) {
	f.visibility, f.slug = visibility, slug
	return Group{ID: groupID, Visibility: visibility, PublicSlug: slug}, nil
}
func (f *fakeRepository) DeleteGroup(_ context.Context, _ serverauth.Principal, groupID string) error {
	f.deleted = groupID
	return nil
}
func (f *fakeRepository) TransferOwnership(_ context.Context, _ serverauth.Principal, _, login string) error {
	f.transferred = login
	return nil
}
func (f *fakeRepository) InvitePreview(context.Context, [32]byte, time.Time) (InvitePreview, error) {
	f.previewed++
	return InvitePreview{GroupName: "Team"}, nil
}

func (f *fakeRepository) CreateGroup(_ context.Context, _ serverauth.Principal, input NewGroup) (Group, error) {
	f.created = input
	return Group{ID: input.ID, Name: input.Name, Timezone: input.Timezone, Role: RoleOwner}, nil
}
func (f *fakeRepository) ListGroups(context.Context, serverauth.Principal) ([]Group, error) {
	return []Group{}, nil
}
func (f *fakeRepository) Group(context.Context, serverauth.Principal, string) (Detail, error) {
	return Detail{}, nil
}
func (f *fakeRepository) CreateInvite(_ context.Context, _ serverauth.Principal, input NewInvite) error {
	f.invite = input
	return nil
}
func (f *fakeRepository) Join(context.Context, serverauth.Principal, [32]byte, time.Time) (Group, error) {
	return Group{}, nil
}
func (f *fakeRepository) Leave(context.Context, serverauth.Principal, string) error { return nil }
func (f *fakeRepository) RevokeMember(context.Context, serverauth.Principal, string, string) error {
	return nil
}
