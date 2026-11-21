package account

import (
	"context"
	"errors"
	"testing"

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
