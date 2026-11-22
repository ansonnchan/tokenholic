package multiplayer

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ansonnchan/tokenholic/internal/account"
)

// ErrSignedOut means multiplayer requires a fresh local login.
var ErrSignedOut = errors.New("multiplayer account is signed out")

// GlobalScope selects the opt-in global board where a group ID is expected.
// Group IDs are UUIDs, so it can never name a group.
const GlobalScope = "global"

// Snapshot is the scope selector and the selected leaderboard loaded
// together. Selected is the zero Group when Global is set.
type Snapshot struct {
	Groups   []Group
	Selected Group
	Global   bool
	Board    *Board
}

type apiClient interface {
	List(context.Context, string) ([]Group, error)
	Create(context.Context, string, string, string) (Group, error)
	Join(context.Context, string, string) (Group, error)
	Invite(context.Context, string, string, time.Duration, int) (Invite, error)
	Leave(context.Context, string, string) error
	Leaderboard(context.Context, string, string, Period, Metric) (Board, error)
	GlobalLeaderboard(context.Context, string, Period, Metric) (Board, error)
	SetGlobalParticipation(context.Context, string, bool) error
	Profile(context.Context, string) (Profile, error)
	SharePage(ctx context.Context, login string) (address string, served bool, err error)
	SetProfileVisibility(context.Context, string, bool) error
	SetGroupVisibility(context.Context, string, string, bool) (Group, error)
	DeleteGroup(context.Context, string, string) error
	TransferGroup(context.Context, string, string, string) error
}

type sessionStore interface {
	Load() (account.Session, error)
}

// Service coordinates stored sessions with private-group API calls.
type Service struct {
	client          apiClient
	store           sessionStore
	defaultTimezone string
}

// NewService constructs the native multiplayer boundary.
func NewService(client apiClient, store sessionStore, defaultTimezone string) (*Service, error) {
	if client == nil || store == nil || defaultTimezone == "" {
		return nil, errors.New("initialize multiplayer: client, session store, and timezone are required")
	}
	return &Service{client: client, store: store, defaultTimezone: defaultTimezone}, nil
}

// Load returns the group list and one selected leaderboard: the group named
// by selectedID, the global board for GlobalScope, and otherwise the first
// group. An account with no groups gets no board unless it asks for the
// global one.
func (s *Service) Load(ctx context.Context, selectedID string, period Period, metric Metric) (Snapshot, error) {
	credential, err := s.credential()
	if err != nil {
		return Snapshot{}, err
	}
	groups, err := s.client.List(ctx, credential)
	if err != nil {
		return Snapshot{}, normalizeSessionError(err)
	}
	snapshot := Snapshot{Groups: groups}
	if selectedID == GlobalScope {
		board, err := s.client.GlobalLeaderboard(ctx, credential, period, metric)
		if err != nil {
			return Snapshot{}, normalizeSessionError(err)
		}
		snapshot.Global = true
		snapshot.Board = &board
		return snapshot, nil
	}
	if len(groups) == 0 {
		return snapshot, nil
	}
	selected := groups[0]
	for _, group := range groups {
		if group.ID == selectedID {
			selected = group
			break
		}
	}
	board, err := s.client.Leaderboard(ctx, credential, selected.ID, period, metric)
	if err != nil {
		return Snapshot{}, normalizeSessionError(err)
	}
	snapshot.Selected = selected
	snapshot.Board = &board
	return snapshot, nil
}

// List returns the caller's active groups for CLI operations.
func (s *Service) List(ctx context.Context) ([]Group, error) {
	credential, err := s.credential()
	if err != nil {
		return nil, err
	}
	groups, err := s.client.List(ctx, credential)
	return groups, normalizeSessionError(err)
}

// Create creates a private group using the local IANA timezone.
func (s *Service) Create(ctx context.Context, name string) (Group, error) {
	credential, err := s.credential()
	if err != nil {
		return Group{}, err
	}
	group, err := s.client.Create(ctx, credential, name, s.defaultTimezone)
	return group, normalizeSessionError(err)
}

// Join consumes one invite code for the current account.
func (s *Service) Join(ctx context.Context, code string) (Group, error) {
	credential, err := s.credential()
	if err != nil {
		return Group{}, err
	}
	group, err := s.client.Join(ctx, credential, code)
	return group, normalizeSessionError(err)
}

// DefaultInviteLifetime and DefaultInviteUses are what an invitation gets
// when the caller does not choose.
const (
	DefaultInviteLifetime = 24 * time.Hour
	DefaultInviteUses     = 10
)

// Invite creates an invitation after resolving a visible group by ID or name.
func (s *Service) Invite(ctx context.Context, groupName string, lifetime time.Duration, maxUses int) (Invite, error) {
	credential, err := s.credential()
	if err != nil {
		return Invite{}, err
	}
	group, err := s.resolveGroup(ctx, credential, groupName)
	if err != nil {
		return Invite{}, err
	}
	invite, err := s.client.Invite(ctx, credential, group.ID, lifetime, maxUses)
	return invite, normalizeSessionError(err)
}

// Leave revokes membership after resolving a visible group by ID or name.
func (s *Service) Leave(ctx context.Context, groupName string) error {
	credential, err := s.credential()
	if err != nil {
		return err
	}
	group, err := s.resolveGroup(ctx, credential, groupName)
	if err != nil {
		return err
	}
	return normalizeSessionError(s.client.Leave(ctx, credential, group.ID))
}

// SetGlobalParticipation joins or leaves the opt-in global board for the
// signed-in account.
func (s *Service) SetGlobalParticipation(ctx context.Context, participating bool) error {
	credential, err := s.credential()
	if err != nil {
		return err
	}
	return normalizeSessionError(s.client.SetGlobalParticipation(ctx, credential, participating))
}

// Profile returns the signed-in account's profile and visibility choices.
func (s *Service) Profile(ctx context.Context) (Profile, error) {
	credential, err := s.credential()
	if err != nil {
		return Profile{}, err
	}
	profile, err := s.client.Profile(ctx, credential)
	return profile, normalizeSessionError(err)
}

// ShareLink says whether a post by the signed-in account can carry a card.
type ShareLink struct {
	// Login is the account's GitHub login.
	Login string
	// URL is the account's share page, set only when the server serves it.
	// Attached to a post, it unfurls with the account's card.
	URL string
	// Private reports that the profile is private, so there is no page.
	Private bool
}

// ShareLink finds the signed-in account's share page. A private profile has
// none, and neither does a server without a website; both leave URL empty.
// The page is fetched once without a credential, exactly as a crawler will,
// so a link is offered only if it works.
func (s *Service) ShareLink(ctx context.Context) (ShareLink, error) {
	profile, err := s.Profile(ctx)
	if err != nil {
		return ShareLink{}, err
	}
	link := ShareLink{Login: profile.Login, Private: profile.Visibility != "public"}
	if link.Private {
		return link, nil
	}
	address, served, err := s.client.SharePage(ctx, profile.Login)
	if err != nil {
		return ShareLink{}, err
	}
	if served {
		link.URL = address
	}
	return link, nil
}

// SetProfileVisibility makes the signed-in account's profile public or private.
func (s *Service) SetProfileVisibility(ctx context.Context, public bool) error {
	credential, err := s.credential()
	if err != nil {
		return err
	}
	return normalizeSessionError(s.client.SetProfileVisibility(ctx, credential, public))
}

// SetGroupVisibility makes a group's board public or private after resolving
// the group by ID or name.
func (s *Service) SetGroupVisibility(ctx context.Context, groupName string, public bool) (Group, error) {
	credential, err := s.credential()
	if err != nil {
		return Group{}, err
	}
	group, err := s.resolveGroup(ctx, credential, groupName)
	if err != nil {
		return Group{}, err
	}
	updated, err := s.client.SetGroupVisibility(ctx, credential, group.ID, public)
	return updated, normalizeSessionError(err)
}

// DeleteGroup permanently deletes a group after resolving it by ID or name.
func (s *Service) DeleteGroup(ctx context.Context, groupName string) error {
	credential, err := s.credential()
	if err != nil {
		return err
	}
	group, err := s.resolveGroup(ctx, credential, groupName)
	if err != nil {
		return err
	}
	return normalizeSessionError(s.client.DeleteGroup(ctx, credential, group.ID))
}

// TransferGroup hands a group to another member after resolving it by ID or
// name.
func (s *Service) TransferGroup(ctx context.Context, groupName, login string) error {
	credential, err := s.credential()
	if err != nil {
		return err
	}
	group, err := s.resolveGroup(ctx, credential, groupName)
	if err != nil {
		return err
	}
	return normalizeSessionError(s.client.TransferGroup(ctx, credential, group.ID, login))
}

func (s *Service) resolveGroup(ctx context.Context, credential, name string) (Group, error) {
	groups, err := s.client.List(ctx, credential)
	if err != nil {
		return Group{}, normalizeSessionError(err)
	}
	var match *Group
	for index := range groups {
		if groups[index].ID == name {
			return groups[index], nil
		}
		if groups[index].Name != name {
			continue
		}
		if match != nil {
			return Group{}, errors.New("group name is ambiguous; use its ID")
		}
		value := groups[index]
		match = &value
	}
	if match == nil {
		return Group{}, ErrNotFound
	}
	return *match, nil
}

func (s *Service) credential() (string, error) {
	session, err := s.store.Load()
	if err != nil {
		if errors.Is(err, account.ErrNoSession) {
			return "", ErrSignedOut
		}
		return "", fmt.Errorf("load multiplayer session: %w", err)
	}
	if session.Credential == "" {
		return "", ErrSignedOut
	}
	return session.Credential, nil
}

func normalizeSessionError(err error) error {
	if errors.Is(err, ErrUnauthorized) {
		return ErrSignedOut
	}
	return err
}
