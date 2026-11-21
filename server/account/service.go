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
}

// Service owns account usage authorization.
type Service struct {
	auth       Authenticator
	repository Repository
}

// NewService constructs the account aggregation service.
func NewService(auth Authenticator, repository Repository) (*Service, error) {
	if auth == nil || repository == nil {
		return nil, errors.New("initialize account usage: authenticator and repository are required")
	}
	return &Service{auth: auth, repository: repository}, nil
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
