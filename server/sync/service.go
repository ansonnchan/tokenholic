package sync

import (
	"context"
	"errors"
	"fmt"

	protocol "github.com/ansonnchan/tokenholic/internal/sync"
	serverauth "github.com/ansonnchan/tokenholic/server/auth"
	"github.com/ansonnchan/tokenholic/server/pricing"
)

var (
	// ErrInvalidBatch means the upload violates the protocol itself. A model
	// the server does not recognize is never a reason for it.
	ErrInvalidBatch = errors.New("usage batch is invalid")
	// ErrRevisionConflict means equal device/key/revision identity was presented
	// with a different allowlisted payload.
	ErrRevisionConflict = errors.New("usage bucket revision conflicts with stored payload")
)

// Authenticator resolves the device represented by a bearer credential.
type Authenticator interface {
	Authenticate(context.Context, string) (serverauth.Principal, error)
}

// Bucket pairs the client's evidence with the values the server derived
// from it. Only the envelope takes part in revision conflict detection.
type Bucket struct {
	Envelope protocol.Envelope
	Derived  pricing.Derived
}

// Repository applies absolute revisions for one authenticated device.
type Repository interface {
	ApplyUsageBuckets(context.Context, serverauth.Principal, []Bucket) ([]protocol.Acknowledgement, error)
}

// Pricer derives the server-owned model and estimate columns.
type Pricer interface {
	Derive(pricing.Usage) (pricing.Derived, error)
}

// Service owns authentication and server-side batch application.
type Service struct {
	auth       Authenticator
	repository Repository
	pricer     Pricer
}

// NewService constructs the synchronization service.
func NewService(auth Authenticator, repository Repository, pricer Pricer) (*Service, error) {
	if auth == nil || repository == nil || pricer == nil {
		return nil, errors.New("initialize synchronization: authenticator, repository, and pricer are required")
	}
	return &Service{auth: auth, repository: repository, pricer: pricer}, nil
}

// Apply authenticates the device, derives model and price for every bucket
// with the server's own catalog, and applies one validated bounded batch.
func (s *Service) Apply(ctx context.Context, credential string, batch protocol.Batch) (protocol.BatchResponse, error) {
	if err := batch.Validate(); err != nil {
		return protocol.BatchResponse{}, fmt.Errorf("%w: %v", ErrInvalidBatch, err)
	}
	principal, err := s.auth.Authenticate(ctx, credential)
	if err != nil {
		return protocol.BatchResponse{}, err
	}
	buckets := make([]Bucket, len(batch.Buckets))
	for index, envelope := range batch.Buckets {
		derived, err := s.pricer.Derive(pricing.Usage{
			ObservedModelID: envelope.Bucket.ObservedModelID, ModelEvidence: envelope.Bucket.ModelEvidence,
			ServiceTier: envelope.Bucket.ServiceTier, PromptBand: envelope.Bucket.PromptBand,
			Tokens: envelope.Bucket.Tokens,
		})
		if err != nil {
			// Derivation fails only on arithmetic overflow, which no real
			// minute of usage can reach.
			return protocol.BatchResponse{}, fmt.Errorf("%w: bucket %d: %v", ErrInvalidBatch, index, err)
		}
		buckets[index] = Bucket{Envelope: envelope, Derived: derived}
	}
	acknowledgements, err := s.repository.ApplyUsageBuckets(ctx, principal, buckets)
	if err != nil {
		return protocol.BatchResponse{}, err
	}
	return protocol.BatchResponse{Acknowledgements: acknowledgements}, nil
}
