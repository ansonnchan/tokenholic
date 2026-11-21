package account

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	serverauth "github.com/ansonnchan/tokenholic/server/auth"
)

func TestDeleteAuthenticatesBeforeRepository(t *testing.T) {
	repository := &fakeRepository{}
	service, err := NewService(fakeAuthenticator{principal: serverauth.Principal{UserID: "user"}}, repository)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Delete(t.Context(), "credential"); err != nil || repository.deletedUser != "user" {
		t.Fatalf("Delete() = %v, user=%q", err, repository.deletedUser)
	}
	unauthorized, err := NewService(fakeAuthenticator{err: serverauth.ErrUnauthorized}, repository)
	if err != nil {
		t.Fatal(err)
	}
	repository.deletedUser = ""
	if err := unauthorized.Delete(t.Context(), "bad"); !errors.Is(err, serverauth.ErrUnauthorized) || repository.deletedUser != "" {
		t.Fatalf("unauthorized Delete() = %v, user=%q", err, repository.deletedUser)
	}
}

func TestSetGlobalOptInAuthenticatesBeforeRepository(t *testing.T) {
	repository := &fakeRepository{}
	service, err := NewService(fakeAuthenticator{principal: serverauth.Principal{UserID: "user"}}, repository)
	if err != nil {
		t.Fatal(err)
	}
	for _, participating := range []bool{true, false} {
		if err := service.SetGlobalOptIn(t.Context(), "credential", participating); err != nil || repository.optInUser != "user" || repository.optIn == nil || *repository.optIn != participating {
			t.Fatalf("SetGlobalOptIn(%t) = %v, stored %v for %q", participating, err, repository.optIn, repository.optInUser)
		}
	}
	unauthorized, err := NewService(fakeAuthenticator{err: serverauth.ErrUnauthorized}, repository)
	if err != nil {
		t.Fatal(err)
	}
	repository.optIn = nil
	if err := unauthorized.SetGlobalOptIn(t.Context(), "bad", true); !errors.Is(err, serverauth.ErrUnauthorized) || repository.optIn != nil {
		t.Fatalf("unauthorized SetGlobalOptIn() = %v, stored %v", err, repository.optIn)
	}
}

func TestSetProfileVisibilityAcceptsOnlyPublicOrPrivate(t *testing.T) {
	repository := &fakeRepository{}
	service, err := NewService(fakeAuthenticator{principal: serverauth.Principal{UserID: "user"}}, repository)
	if err != nil {
		t.Fatal(err)
	}
	for _, visibility := range []Visibility{VisibilityPublic, VisibilityPrivate} {
		if err := service.SetProfileVisibility(t.Context(), "credential", visibility); err != nil || repository.visibility != visibility {
			t.Fatalf("SetProfileVisibility(%q) = %v, stored %q", visibility, err, repository.visibility)
		}
	}
	for _, invalid := range []Visibility{"", "Public", "unlisted", "true"} {
		if err := service.SetProfileVisibility(t.Context(), "credential", invalid); !errors.Is(err, ErrInvalidVisibility) || repository.visibility != VisibilityPrivate {
			t.Fatalf("SetProfileVisibility(%q) = %v, stored %q; want it refused", invalid, err, repository.visibility)
		}
	}
	unauthorized, err := NewService(fakeAuthenticator{err: serverauth.ErrUnauthorized}, repository)
	if err != nil {
		t.Fatal(err)
	}
	if err := unauthorized.SetProfileVisibility(t.Context(), "bad", VisibilityPublic); !errors.Is(err, serverauth.ErrUnauthorized) || repository.visibility != VisibilityPrivate {
		t.Fatalf("unauthorized SetProfileVisibility() = %v, stored %q", err, repository.visibility)
	}
	if _, err := unauthorized.Profile(t.Context(), "bad"); !errors.Is(err, serverauth.ErrUnauthorized) {
		t.Fatalf("unauthorized Profile() = %v", err)
	}
}

// A public profile read needs no credential. A login that could not be a
// GitHub login gets the same answer as an unknown one and never reaches
// storage.
func TestPublicProfileTreatsEveryMissReadAlike(t *testing.T) {
	repository := &fakeRepository{}
	service, err := NewService(fakeAuthenticator{err: serverauth.ErrUnauthorized}, repository)
	if err != nil {
		t.Fatal(err)
	}
	if profile, err := service.PublicProfile(t.Context(), "public-member"); err != nil || profile.Login != "public-member" {
		t.Fatalf("PublicProfile() = %#v, %v", profile, err)
	}
	for _, login := range []string{"private-member", "nobody", "", "-leading", "trailing-", "double--hyphen", "has space", "has/slash", "../etc", strings.Repeat("a", 40), "üser"} {
		if _, err := service.PublicProfile(t.Context(), login); !errors.Is(err, ErrProfileNotFound) {
			t.Fatalf("PublicProfile(%q) error = %v, want the one not-found error", login, err)
		}
	}
	if got := strings.Join(repository.publicLookups, ","); got != "public-member,private-member,nobody" {
		t.Fatalf("logins that reached storage = %q", got)
	}
}

type fakeAuthenticator struct {
	principal serverauth.Principal
	err       error
}

func (f fakeAuthenticator) Authenticate(context.Context, string) (serverauth.Principal, error) {
	return f.principal, f.err
}

type fakeRepository struct {
	deletedUser string
	optInUser   string
	optIn       *bool
	visibility  Visibility
	// publicLookups records every login that reached storage.
	publicLookups []string
}

func (f *fakeRepository) Profile(context.Context, serverauth.Principal) (Profile, error) {
	return Profile{Login: "member"}, nil
}

func (f *fakeRepository) SetProfileVisibility(_ context.Context, _ serverauth.Principal, visibility Visibility) error {
	f.visibility = visibility
	return nil
}

func (f *fakeRepository) PublicProfile(_ context.Context, login string, _ time.Time) (PublicProfile, error) {
	f.publicLookups = append(f.publicLookups, login)
	if login != "public-member" {
		return PublicProfile{}, ErrProfileNotFound
	}
	return PublicProfile{Login: login}, nil
}

func (f *fakeRepository) SetGlobalOptIn(_ context.Context, principal serverauth.Principal, participating bool) error {
	f.optInUser, f.optIn = principal.UserID, &participating
	return nil
}

func (f *fakeRepository) AccountUsage(context.Context, serverauth.Principal) (Usage, error) {
	return Usage{}, nil
}

func (f *fakeRepository) DeleteAccount(_ context.Context, principal serverauth.Principal) error {
	f.deletedUser = principal.UserID
	return nil
}
