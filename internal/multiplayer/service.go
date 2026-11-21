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

// Snapshot is the group selector and selected leaderboard loaded together.
type Snapshot struct {
	Groups   []Group
	Selected Group
	Board    *Board
}

type apiClient interface {
	List(context.Context, string) ([]Group, error)
	Create(context.Context, string, string, string) (Group, error)
	Join(context.Context, string, string) (Group, error)
	Invite(context.Context, string, string, time.Duration, int) (Invite, error)
	Leave(context.Context, string, string) error
	Leaderboard(context.Context, string, string, Period, Metric) (Board, error)
	SetGlobalParticipation(context.Context, string, bool) error
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

// Load returns the group list and one selected leaderboard.
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
