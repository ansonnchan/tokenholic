package account

import (
	"context"
	"errors"
	"time"

	serverauth "github.com/ansonnchan/tokenholic/server/auth"
)

// Availability preserves complete, partial, unavailable, and empty coverage.
type Availability string

const (
	AvailabilityEmpty       Availability = "empty"
	AvailabilityComplete    Availability = "complete"
	AvailabilityPartial     Availability = "partial"
	AvailabilityUnavailable Availability = "unavailable"
)

// Value is an arbitrary-precision decimal aggregate plus coverage.
type Value struct {
	Value *string      `json:"value"`
	State Availability `json:"state"`
}

// Tokens is the account-level normalized token breakdown.
type Tokens struct {
	InputUncached   Value `json:"input_uncached"`
	InputCacheRead  Value `json:"input_cache_read"`
	InputCacheWrite Value `json:"input_cache_write"`
	Output          Value `json:"output"`
	ReasoningOutput Value `json:"reasoning_output"`
}

// Device reports independent revocation and freshness without exposing local
// machine or project identity.
type Device struct {
	DeviceID     string     `json:"device_id"`
	Revoked      bool       `json:"revoked"`
	LastSeenAt   *time.Time `json:"last_seen_at"`
	LastSyncedAt *time.Time `json:"last_synced_at"`
}

// Usage is one account row aggregated from active devices.
type Usage struct {
	UserID        string     `json:"user_id"`
	DisplayName   string     `json:"display_name"`
	GeneratedAt   time.Time  `json:"generated_at"`
	ActiveDevices int        `json:"active_devices"`
	LastSyncedAt  *time.Time `json:"last_synced_at"`
	TotalTokens   Value      `json:"total_tokens"`
	Tokens        Tokens     `json:"tokens"`
	APIEstimate   Value      `json:"api_equivalent_estimate_microusd"`
	Devices       []Device   `json:"devices"`
	// GlobalOptIn reports whether the account is listed on the global board.
	GlobalOptIn bool `json:"global_opt_in"`
}

// Visibility says whether a profile can be read without signing in.
type Visibility string

const (
	// VisibilityPrivate is the default: the profile exists only for its owner
	// and their fellow group members.
	VisibilityPrivate Visibility = "private"
	// VisibilityPublic lets anyone read the profile by GitHub login.
	VisibilityPublic Visibility = "public"
)

// ErrProfileNotFound is the single answer for a public profile that is
// private, deleted, or was never created. Callers must not refine it: telling
// those cases apart would reveal that a private account exists.
var ErrProfileNotFound = errors.New("profile not found")

// Profile is the signed-in account's own identity and visibility choices.
type Profile struct {
	Login       string     `json:"login"`
	AvatarURL   string     `json:"avatar_url,omitempty"`
	Visibility  Visibility `json:"visibility"`
	GlobalOptIn bool       `json:"global_opt_in"`
}

// PublicTotals is one window of a public profile. Priced and unpriced tokens
// let a reader see how much of the estimate's usage has a price.
type PublicTotals struct {
	TotalTokens    Value  `json:"total_tokens"`
	APIEstimate    Value  `json:"api_equivalent_estimate_microusd"`
	PricedTokens   string `json:"priced_tokens"`
	UnpricedTokens string `json:"unpriced_tokens"`
}

// PublicProfile is everything a public profile shows: the GitHub login and
// avatar, and aggregate totals over UTC windows. It carries no account ID,
// device, group, model, or timing detail.
type PublicProfile struct {
	Login       string       `json:"login"`
	AvatarURL   string       `json:"avatar_url,omitempty"`
	GeneratedAt time.Time    `json:"generated_at"`
	Last30Days  PublicTotals `json:"last_30_days"`
	AllTime     PublicTotals `json:"all_time"`
}

// Authenticator resolves a tokenholic device credential.
type Authenticator interface {
	Authenticate(context.Context, string) (serverauth.Principal, error)
}

// Repository aggregates the authenticated user's active devices and stores
// the account's own visibility choices.
type Repository interface {
	AccountUsage(context.Context, serverauth.Principal) (Usage, error)
	DeleteAccount(context.Context, serverauth.Principal) error
	SetGlobalOptIn(context.Context, serverauth.Principal, bool) error
	Profile(context.Context, serverauth.Principal) (Profile, error)
	SetProfileVisibility(context.Context, serverauth.Principal, Visibility) error
	// PublicProfile returns ErrProfileNotFound unless the login belongs to a
	// live account whose profile is public.
	PublicProfile(ctx context.Context, login string, generatedAt time.Time) (PublicProfile, error)
}

// Service owns account usage authorization.
type Service struct {
	auth       Authenticator
	repository Repository
	now        func() time.Time
}

// NewService constructs the account aggregation service.
func NewService(auth Authenticator, repository Repository) (*Service, error) {
	if auth == nil || repository == nil {
		return nil, errors.New("initialize account usage: authenticator and repository are required")
	}
	return &Service{auth: auth, repository: repository, now: time.Now}, nil
}

// Usage authenticates one device and aggregates its user account.
func (s *Service) Usage(ctx context.Context, credential string) (Usage, error) {
	principal, err := s.auth.Authenticate(ctx, credential)
	if err != nil {
		return Usage{}, err
	}
	return s.repository.AccountUsage(ctx, principal)
}

// Delete authenticates one device and permanently erases its account-owned
// server state. Local source data remains on each device.
func (s *Service) Delete(ctx context.Context, credential string) error {
	principal, err := s.auth.Authenticate(ctx, credential)
	if err != nil {
		return err
	}
	return s.repository.DeleteAccount(ctx, principal)
}

// SetGlobalOptIn authenticates one device and joins or leaves the global
// board for its account. The choice belongs to the account, not the device,
// and takes effect on the next board read. Repeating it changes nothing.
func (s *Service) SetGlobalOptIn(ctx context.Context, credential string, participating bool) error {
	principal, err := s.auth.Authenticate(ctx, credential)
	if err != nil {
		return err
	}
	return s.repository.SetGlobalOptIn(ctx, principal, participating)
}

// Profile authenticates one device and returns its account's own profile.
func (s *Service) Profile(ctx context.Context, credential string) (Profile, error) {
	principal, err := s.auth.Authenticate(ctx, credential)
	if err != nil {
		return Profile{}, err
	}
	return s.repository.Profile(ctx, principal)
}

// SetProfileVisibility authenticates one device and makes its account's
// profile public or private. Anything other than those two values is refused,
// so a malformed request can never publish a profile.
func (s *Service) SetProfileVisibility(ctx context.Context, credential string, visibility Visibility) error {
	principal, err := s.auth.Authenticate(ctx, credential)
	if err != nil {
		return err
	}
	if visibility != VisibilityPrivate && visibility != VisibilityPublic {
		return ErrInvalidVisibility
	}
	return s.repository.SetProfileVisibility(ctx, principal, visibility)
}

// ErrInvalidVisibility means a visibility was neither public nor private.
var ErrInvalidVisibility = errors.New("visibility must be public or private")

// PublicProfile reads a profile without authentication. A login that is not a
// possible GitHub login gets the same ErrProfileNotFound as a private or
// missing one, and never reaches storage.
func (s *Service) PublicProfile(ctx context.Context, login string) (PublicProfile, error) {
	if !validLogin(login) {
		return PublicProfile{}, ErrProfileNotFound
	}
	return s.repository.PublicProfile(ctx, login, s.now().UTC())
}

// validLogin accepts the shape of a GitHub login: 1 to 39 letters, digits, or
// single hyphens, not starting or ending with a hyphen.
func validLogin(login string) bool {
	if len(login) < 1 || len(login) > 39 || login[0] == '-' || login[len(login)-1] == '-' {
		return false
	}
	for index := 0; index < len(login); index++ {
		character := login[index]
		letter := character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9'
		if !letter && (character != '-' || login[index-1] == '-') {
			return false
		}
	}
	return true
}
