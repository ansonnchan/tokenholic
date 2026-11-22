package group

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	serverauth "github.com/ansonnchan/tokenholic/server/auth"
)

const (
	minimumInviteLifetime = 5 * time.Minute
	maximumInviteLifetime = 30 * 24 * time.Hour
	maximumInviteUses     = 100
)

var (
	// ErrNotFound hides groups that the authenticated user cannot read.
	ErrNotFound = errors.New("group not found")
	// ErrForbidden means a visible group operation requires its owner.
	ErrForbidden = errors.New("group operation is forbidden")
	// ErrInvalidInvite means an invite is unknown, expired, revoked, or exhausted.
	ErrInvalidInvite = errors.New("group invite is invalid")
	// ErrOwnerMustRemain prevents a group from being left without an owner.
	ErrOwnerMustRemain = errors.New("group owner must remain")
	// ErrAlreadyMember means an active member attempted to join again.
	ErrAlreadyMember = errors.New("user is already a group member")
	// ErrGroupFull means the private-group member limit has been reached.
	ErrGroupFull = errors.New("group member limit has been reached")
	// ErrInvalidInput means a group request failed public input validation.
	ErrInvalidInput = errors.New("invalid group input")
	// ErrNotMember means an ownership transfer named someone who is not an
	// active member of the group. Only the owner can reach it, and the owner
	// can already list the members.
	ErrNotMember = errors.New("transfer target is not an active group member")
)

// Visibility says whether a group has a public, read-only board.
type Visibility string

const (
	// VisibilityPrivate is the default: only members can see the group.
	VisibilityPrivate Visibility = "private"
	// VisibilityPublic exposes a read-only board at the group's public slug.
	VisibilityPublic Visibility = "public"
)

// Role is a private-group authorization role.
type Role string

const (
	RoleOwner  Role = "owner"
	RoleMember Role = "member"
)

// Group is a group visible to one of its active members. PublicSlug is set
// once the group has ever been public and names its board only while
// Visibility is public.
type Group struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Timezone   string     `json:"timezone"`
	Role       Role       `json:"role"`
	Visibility Visibility `json:"visibility"`
	PublicSlug string     `json:"public_slug,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
}

// Member is one active group member.
type Member struct {
	UserID      string    `json:"user_id"`
	DisplayName string    `json:"display_name"`
	Role        Role      `json:"role"`
	JoinedAt    time.Time `json:"joined_at"`
}

// Detail is a group and its active membership list.
type Detail struct {
	Group   Group    `json:"group"`
	Members []Member `json:"members"`
}

// Invite is the one-time plaintext result of creating an invitation. Link is
// the same code as a URL to the website's invite page, present only when the
// server knows the website's address. The code sits in the URL fragment, which
// browsers never send to a server.
type Invite struct {
	Code      string    `json:"code"`
	Link      string    `json:"link,omitempty"`
	ExpiresAt time.Time `json:"expires_at"`
	MaxUses   int       `json:"max_uses"`
}

// InvitePreview is what the bearer of a usable invite code may learn before
// signing in: which group it joins and until when.
type InvitePreview struct {
	GroupName string    `json:"group_name"`
	ExpiresAt time.Time `json:"expires_at"`
}

// NewGroup is validated group creation input.
type NewGroup struct {
	ID       string
	Name     string
	Timezone string
}

// NewInvite is an invite record whose plaintext code has already been hashed.
type NewInvite struct {
	ID        string
	GroupID   string
	CodeHash  [32]byte
	ExpiresAt time.Time
	MaxUses   int
}

// Authenticator verifies a tokenholic device credential.
type Authenticator interface {
	Authenticate(context.Context, string) (serverauth.Principal, error)
}

// Repository persists private groups and enforces membership authorization.
type Repository interface {
	CreateGroup(context.Context, serverauth.Principal, NewGroup) (Group, error)
	ListGroups(context.Context, serverauth.Principal) ([]Group, error)
	Group(context.Context, serverauth.Principal, string) (Detail, error)
	CreateInvite(context.Context, serverauth.Principal, NewInvite) error
	Join(context.Context, serverauth.Principal, [32]byte, time.Time) (Group, error)
	Leave(context.Context, serverauth.Principal, string) error
	RevokeMember(context.Context, serverauth.Principal, string, string) error
	// SetVisibility is owner-only. slug is the candidate public slug, used
	// only if the group has none yet.
	SetVisibility(ctx context.Context, principal serverauth.Principal, groupID string, visibility Visibility, slug string) (Group, error)
	// DeleteGroup is owner-only and removes memberships and invites with it.
	DeleteGroup(context.Context, serverauth.Principal, string) error
	// TransferOwnership is owner-only. The new owner is the active member
	// with the given GitHub login.
	TransferOwnership(ctx context.Context, principal serverauth.Principal, groupID, login string) error
	// InvitePreview returns ErrInvalidInvite unless the code is usable now.
	InvitePreview(ctx context.Context, codeHash [32]byte, now time.Time) (InvitePreview, error)
}

// Service authenticates group operations and validates external input.
type Service struct {
	authenticator Authenticator
	repository    Repository
	now           func() time.Time
	// inviteLink builds the website address for an invite code; nil means
	// invitations carry no link.
	inviteLink func(code string) string
}

// SetInviteLinks makes each new invitation carry a link built by link, which
// receives the plaintext code. The API passes the website's invite page.
// Without it invitations carry the code only.
func (s *Service) SetInviteLinks(link func(code string) string) {
	s.inviteLink = link
}

// NewService constructs the private-group application boundary.
func NewService(authenticator Authenticator, repository Repository) (*Service, error) {
	if authenticator == nil || repository == nil {
		return nil, errors.New("initialize groups: authenticator and repository are required")
	}
	return &Service{authenticator: authenticator, repository: repository, now: time.Now}, nil
}

// Create creates a private group with the caller as its owner.
func (s *Service) Create(ctx context.Context, credential, name, timezone string) (Group, error) {
	principal, err := s.authenticate(ctx, credential)
	if err != nil {
		return Group{}, err
	}
	name = strings.TrimSpace(name)
	timezone = strings.TrimSpace(timezone)
	if !validName(name) {
		return Group{}, fmt.Errorf("%w: group name must contain 1 to 80 characters", ErrInvalidInput)
	}
	if len(timezone) < 1 || len(timezone) > 128 {
		return Group{}, fmt.Errorf("%w: group timezone is required", ErrInvalidInput)
	}
	if _, err := time.LoadLocation(timezone); err != nil {
		return Group{}, fmt.Errorf("%w: group timezone must be a known IANA location", ErrInvalidInput)
	}
	id, err := randomUUID()
	if err != nil {
		return Group{}, err
	}
	return s.repository.CreateGroup(ctx, principal, NewGroup{ID: id, Name: name, Timezone: timezone})
}

// List returns only groups in which the caller has an active membership.
func (s *Service) List(ctx context.Context, credential string) ([]Group, error) {
	principal, err := s.authenticate(ctx, credential)
	if err != nil {
		return nil, err
	}
	return s.repository.ListGroups(ctx, principal)
}

// Get returns one group and its active members after membership authorization.
func (s *Service) Get(ctx context.Context, credential, groupID string) (Detail, error) {
	principal, err := s.authenticate(ctx, credential)
	if err != nil {
		return Detail{}, err
	}
	if !validUUID(groupID) {
		return Detail{}, ErrNotFound
	}
	return s.repository.Group(ctx, principal, groupID)
}

// Invite creates an expiring, usage-limited code. Only the group owner may call it.
func (s *Service) Invite(ctx context.Context, credential, groupID string, lifetime time.Duration, maxUses int) (Invite, error) {
	principal, err := s.authenticate(ctx, credential)
	if err != nil {
		return Invite{}, err
	}
	if !validUUID(groupID) {
		return Invite{}, ErrNotFound
	}
	if lifetime < minimumInviteLifetime || lifetime > maximumInviteLifetime {
		return Invite{}, fmt.Errorf("%w: invite lifetime must be between 5 minutes and 30 days", ErrInvalidInput)
	}
	if maxUses < 1 || maxUses > maximumInviteUses {
		return Invite{}, fmt.Errorf("%w: invite use limit must be between 1 and 100", ErrInvalidInput)
	}
	id, err := randomUUID()
	if err != nil {
		return Invite{}, err
	}
	codeBytes := make([]byte, 24)
	if _, err := rand.Read(codeBytes); err != nil {
		return Invite{}, fmt.Errorf("generate group invite: %w", err)
	}
	code := "ovt_" + base64.RawURLEncoding.EncodeToString(codeBytes)
	hash := sha256.Sum256([]byte(code))
	// Storage keeps microseconds. Truncating here makes the expiry returned
	// now the same instant every later read reports.
	expiresAt := s.now().UTC().Add(lifetime).Truncate(time.Microsecond)
	if err := s.repository.CreateInvite(ctx, principal, NewInvite{
		ID: id, GroupID: groupID, CodeHash: hash, ExpiresAt: expiresAt, MaxUses: maxUses,
	}); err != nil {
		return Invite{}, err
	}
	invite := Invite{Code: code, ExpiresAt: expiresAt, MaxUses: maxUses}
	if s.inviteLink != nil {
		invite.Link = s.inviteLink(code)
	}
	return invite, nil
}

// InvitePreview tells the bearer of a usable invite code which group it
// joins, without authentication. An unknown, expired, exhausted, revoked, or
// malformed code all get ErrInvalidInvite.
func (s *Service) InvitePreview(ctx context.Context, code string) (InvitePreview, error) {
	if !validInviteCode(code) {
		return InvitePreview{}, ErrInvalidInvite
	}
	return s.repository.InvitePreview(ctx, sha256.Sum256([]byte(code)), s.now().UTC())
}

// SetVisibility makes a group's board public or private. Only the owner may
// call it. The first time a group becomes public it is given a random slug,
// so the board's address reveals nothing about the group and cannot be
// guessed from its name.
func (s *Service) SetVisibility(ctx context.Context, credential, groupID string, visibility Visibility) (Group, error) {
	principal, err := s.authenticate(ctx, credential)
	if err != nil {
		return Group{}, err
	}
	if !validUUID(groupID) {
		return Group{}, ErrNotFound
	}
	if visibility != VisibilityPrivate && visibility != VisibilityPublic {
		return Group{}, fmt.Errorf("%w: visibility must be public or private", ErrInvalidInput)
	}
	suffix := make([]byte, 5)
	if _, err := rand.Read(suffix); err != nil {
		return Group{}, fmt.Errorf("generate group slug: %w", err)
	}
	return s.repository.SetVisibility(ctx, principal, groupID, visibility, "g-"+strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(suffix)))
}

// Delete permanently removes a group with its memberships and invitations.
// Only the owner may call it. Members' usage is untouched.
func (s *Service) Delete(ctx context.Context, credential, groupID string) error {
	principal, err := s.authenticate(ctx, credential)
	if err != nil {
		return err
	}
	if !validUUID(groupID) {
		return ErrNotFound
	}
	return s.repository.DeleteGroup(ctx, principal, groupID)
}

// Transfer hands ownership to another active member, named by GitHub login,
// and makes the previous owner an ordinary member. Only the owner may call it.
func (s *Service) Transfer(ctx context.Context, credential, groupID, login string) error {
	principal, err := s.authenticate(ctx, credential)
	if err != nil {
		return err
	}
	if !validUUID(groupID) {
		return ErrNotFound
	}
	login = strings.TrimSpace(login)
	if len(login) < 1 || len(login) > 39 {
		return fmt.Errorf("%w: a GitHub login is required", ErrInvalidInput)
	}
	return s.repository.TransferOwnership(ctx, principal, groupID, login)
}

// Join consumes one use of an active invite and creates a member role.
func (s *Service) Join(ctx context.Context, credential, code string) (Group, error) {
	principal, err := s.authenticate(ctx, credential)
	if err != nil {
		return Group{}, err
	}
	if !validInviteCode(code) {
		return Group{}, ErrInvalidInvite
	}
	hash := sha256.Sum256([]byte(code))
	return s.repository.Join(ctx, principal, hash, s.now().UTC())
}

// Leave revokes the caller's member role. Owners cannot leave their group.
func (s *Service) Leave(ctx context.Context, credential, groupID string) error {
	principal, err := s.authenticate(ctx, credential)
	if err != nil {
		return err
	}
	if !validUUID(groupID) {
		return ErrNotFound
	}
	return s.repository.Leave(ctx, principal, groupID)
}

// Revoke removes an active member. Only the owner may call it.
func (s *Service) Revoke(ctx context.Context, credential, groupID, userID string) error {
	principal, err := s.authenticate(ctx, credential)
	if err != nil {
		return err
	}
	if !validUUID(groupID) {
		return ErrNotFound
	}
	if !validUUID(userID) {
		return ErrForbidden
	}
	return s.repository.RevokeMember(ctx, principal, groupID, userID)
}

func (s *Service) authenticate(ctx context.Context, credential string) (serverauth.Principal, error) {
	if strings.TrimSpace(credential) == "" {
		return serverauth.Principal{}, serverauth.ErrUnauthorized
	}
	return s.authenticator.Authenticate(ctx, credential)
}

func validInviteCode(code string) bool {
	return strings.HasPrefix(code, "ovt_") && len(code) == 36
}

func randomUUID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate group identifier: %w", err)
	}
	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", raw[0:4], raw[4:6], raw[6:8], raw[8:10], raw[10:16]), nil
}

func validUUID(value string) bool {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return false
	}
	for index, character := range value {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			continue
		}
		if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f')) {
			return false
		}
	}
	return true
}

func validName(value string) bool {
	if !utf8.ValidString(value) || utf8.RuneCountInString(value) < 1 || utf8.RuneCountInString(value) > 80 {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}
