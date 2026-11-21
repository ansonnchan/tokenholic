package account

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestManagerLoginRefreshAndLogout(t *testing.T) {
	session := Session{
		Credential: "credential", UserID: "user", DeviceID: "device",
		Login: "synthetic-user", ExpiresAt: time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	client := &fakeClient{
		start:     DeviceStart{FlowToken: "flow", UserCode: "ABCD", VerificationURI: "https://github.com/login/device", ExpiresIn: time.Minute, Interval: time.Second},
		polls:     []PollResult{{Pending: true, RetryAfter: 2 * time.Second}, {Session: session}},
		refreshed: Session{Credential: "replacement", UserID: "user", DeviceID: "device", ExpiresAt: session.ExpiresAt.Add(time.Hour)},
	}
	store := &memoryStore{}
	manager, err := NewManager(client, store)
	if err != nil {
		t.Fatal(err)
	}
	var waits []time.Duration
	manager.sleep = func(_ context.Context, duration time.Duration) error {
		waits = append(waits, duration)
		return nil
	}
	var output bytes.Buffer
	if err := manager.Login(t.Context(), "test", &output); err != nil {
		t.Fatal(err)
	}
	if store.session.Credential != "credential" || !strings.Contains(output.String(), "ABCD") || !strings.Contains(output.String(), "synthetic-user") {
		t.Fatalf("login store/output = %#v/%q", store.session, output.String())
	}
	if len(waits) != 2 || waits[0] != time.Second || waits[1] != 2*time.Second {
		t.Fatalf("login waits = %v", waits)
	}
	if err := manager.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	if store.session.Credential != "replacement" || store.session.Login != "synthetic-user" {
		t.Fatalf("refreshed session = %#v", store.session)
	}
	if err := manager.Logout(t.Context()); err != nil || !store.deleted || client.logoutCredential != "replacement" {
		t.Fatalf("Logout() = %v, deleted=%v credential=%q", err, store.deleted, client.logoutCredential)
	}
}

func TestBeginShowsTheCodeAndCompleteStoresOnlyAfterAuthorization(t *testing.T) {
	session := Session{Credential: "credential", UserID: "user", DeviceID: "device", Login: "synthetic-user"}
	challenge := DeviceStart{FlowToken: "flow", UserCode: "ABCD-1234", VerificationURI: "https://github.com/login/device", ExpiresIn: time.Minute, Interval: time.Second}
	client := &fakeClient{start: challenge, polls: []PollResult{{Pending: true}, {Pending: true}, {Session: session}}}
	store := &memoryStore{}
	manager, err := NewManager(client, store)
	if err != nil {
		t.Fatal(err)
	}
	started, err := manager.Begin(t.Context(), "test")
	if err != nil || started != challenge {
		t.Fatalf("Begin() = %#v, %v", started, err)
	}
	if store.session.Credential != "" {
		t.Fatal("Begin stored a credential before the user authorized anything")
	}

	// Cancelling while the user is still at GitHub stores nothing.
	ctx, cancel := context.WithCancel(t.Context())
	waited := 0
	manager.sleep = func(ctx context.Context, _ time.Duration) error {
		waited++
		if waited == 2 {
			cancel()
		}
		return ctx.Err()
	}
	if _, err := manager.Complete(ctx, started); err == nil || store.session.Credential != "" {
		t.Fatalf("cancelled Complete() = %v with stored %#v", err, store.session)
	}

	manager.sleep = func(context.Context, time.Duration) error { return nil }
	client.polls = []PollResult{{Pending: true}, {Session: session}}
	got, err := manager.Complete(t.Context(), started)
	if err != nil || got != session || store.session != session {
		t.Fatalf("Complete() = %#v, %v with stored %#v", got, err, store.session)
	}

	// A challenge that has already expired never polls.
	client.polls = nil
	started.ExpiresIn = 0
	manager.sleep = sleepContext
	if _, err := manager.Complete(t.Context(), started); err == nil {
		t.Fatal("Complete() accepted an expired challenge")
	}
}

func TestManagerKeepsCredentialWhenRevocationFails(t *testing.T) {
	client := &fakeClient{logoutErr: errors.New("network unavailable")}
	store := &memoryStore{session: Session{Credential: "still-live"}}
	manager, err := NewManager(client, store)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Logout(t.Context()); err == nil || store.deleted {
		t.Fatalf("Logout() = %v, deleted=%v", err, store.deleted)
	}
}

func TestManagerDeletesLocalCredentialOnlyAfterHostedDeletion(t *testing.T) {
	client := &fakeClient{}
	store := &memoryStore{session: Session{Credential: "delete-credential"}}
	manager, err := NewManager(client, store)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.DeleteAccount(t.Context()); err != nil || !store.deleted || client.deleteCredential != "delete-credential" {
		t.Fatalf("DeleteAccount() = %v, deleted=%v credential=%q", err, store.deleted, client.deleteCredential)
	}
	client = &fakeClient{deleteErr: errors.New("server unavailable")}
	store = &memoryStore{session: Session{Credential: "keep-credential"}}
	manager, err = NewManager(client, store)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.DeleteAccount(t.Context()); err == nil || store.deleted {
		t.Fatalf("failed DeleteAccount() = %v, deleted=%v", err, store.deleted)
	}
}

type fakeClient struct {
	start            DeviceStart
	polls            []PollResult
	refreshed        Session
	logoutErr        error
	logoutCredential string
	deleteErr        error
	deleteCredential string
}

func (f *fakeClient) Start(context.Context, string) (DeviceStart, error) { return f.start, nil }
func (f *fakeClient) Poll(context.Context, string) (PollResult, error) {
	result := f.polls[0]
	f.polls = f.polls[1:]
	return result, nil
}
func (f *fakeClient) Refresh(context.Context, string) (Session, error) { return f.refreshed, nil }
func (f *fakeClient) Logout(_ context.Context, credential string) error {
	f.logoutCredential = credential
	return f.logoutErr
}
func (f *fakeClient) DeleteAccount(_ context.Context, credential string) error {
	f.deleteCredential = credential
	return f.deleteErr
}

type memoryStore struct {
	session Session
	deleted bool
}

func (s *memoryStore) Load() (Session, error) {
	if s.session.Credential == "" {
		return Session{}, ErrNoSession
	}
	return s.session, nil
}
func (s *memoryStore) Save(session Session) error { s.session = session; return nil }
func (s *memoryStore) Delete() error              { s.deleted = true; s.session = Session{}; return nil }
