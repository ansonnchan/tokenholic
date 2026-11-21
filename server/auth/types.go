package auth

import (
	"context"
	"errors"
	"time"
)

var (
	// ErrInvalidInput means an authentication request contains invalid client data.
	ErrInvalidInput = errors.New("authentication input is invalid")
	// ErrUnauthorized means a tokenholic device credential is absent, invalid,
	// expired, or revoked.
	ErrUnauthorized = errors.New("device credential is unauthorized")
)

// FlowStatus is the current GitHub device authorization state.
type FlowStatus string

const (
	FlowPending  FlowStatus = "pending"
	FlowSlowDown FlowStatus = "slow_down"
	FlowApproved FlowStatus = "approved"
	FlowExpired  FlowStatus = "expired"
	FlowDenied   FlowStatus = "denied"
)

// DeviceAuthorization is GitHub's short-lived device authorization response.
type DeviceAuthorization struct {
	DeviceCode      string
	UserCode        string
	VerificationURI string
	ExpiresAt       time.Time
	Interval        time.Duration
}

// GitHubPollResult is one device authorization poll result. AccessToken is
// populated only for an approved result and must never be persisted.
type GitHubPollResult struct {
	Status      FlowStatus
	AccessToken string
	RetryAfter  time.Duration
}

// GitHubUser is the minimal stable identity tokenholic retains from GitHub.
//
// AvatarURL is GitHub's image URL for the account, or empty when GitHub
// supplied none tokenholic accepts. It is shown to fellow group members and,
// only with a public profile, publicly.
type GitHubUser struct {
	ID        int64
	Login     string
	Name      string
	AvatarURL string
}

// GitHub exchanges device codes and resolves the authenticated identity.
type GitHub interface {
	Start(context.Context) (DeviceAuthorization, error)
	Poll(context.Context, string) (GitHubPollResult, error)
	User(context.Context, string) (GitHubUser, error)
}

// CredentialProof is the parsed identity and one-way hash of a presented
// tokenholic device credential.
type CredentialProof struct {
	CredentialID string
	SecretHash   [32]byte
}

// NewCredential is a newly generated tokenholic credential ready for persistence.
type NewCredential struct {
	CredentialID string
	SecretHash   [32]byte
	ExpiresAt    time.Time
}

// DeviceRegistration identifies a new independently revocable device.
type DeviceRegistration struct {
	UserID        string
	DeviceID      string
	ClientVersion string
	Credential    NewCredential
}

// Identity is the server-side account and device identity.
type Identity struct {
	UserID   string
	DeviceID string
	Login    string
}

// Principal is an authenticated tokenholic device.
type Principal struct {
	UserID       string
	DeviceID     string
	CredentialID string
}

// Repository persists accounts and verifies, rotates, and revokes credentials.
type Repository interface {
	RegisterDevice(context.Context, GitHubUser, DeviceRegistration) (Identity, error)
	Authenticate(context.Context, CredentialProof) (Principal, error)
	RotateCredential(context.Context, CredentialProof, NewCredential) (Principal, error)
	RevokeDevice(context.Context, CredentialProof) error
}

// StartResponse is returned to a CLI beginning device authorization.
type StartResponse struct {
	FlowToken       string        `json:"flow_token"`
	UserCode        string        `json:"user_code"`
	VerificationURI string        `json:"verification_uri"`
	ExpiresIn       time.Duration `json:"-"`
	Interval        time.Duration `json:"-"`
}

// PollResponse is either pending or an authenticated device session.
type PollResponse struct {
	Status     FlowStatus
	RetryAfter time.Duration
	Session    *Session
}

// Session is the only authentication state the native client retains.
type Session struct {
	Credential string    `json:"credential"`
	UserID     string    `json:"user_id"`
	DeviceID   string    `json:"device_id"`
	Login      string    `json:"login"`
	ExpiresAt  time.Time `json:"expires_at"`
}
