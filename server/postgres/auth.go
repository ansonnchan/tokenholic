package postgres

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"

	serverauth "github.com/ansonnchan/tokenholic/server/auth"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// AuthRepository persists account identity and independently revocable device
// credentials. GitHub access tokens never cross this boundary.
type AuthRepository struct {
	pool *pgxpool.Pool
}

// NewAuthRepository constructs the PostgreSQL authentication repository.
func NewAuthRepository(pool *pgxpool.Pool) (*AuthRepository, error) {
	if pool == nil {
		return nil, errors.New("initialize authentication repository: pool is required")
	}
	return &AuthRepository{pool: pool}, nil
}

// RegisterDevice maps one stable GitHub identity to one user and creates a new
// device plus its initial credential atomically.
func (r *AuthRepository) RegisterDevice(ctx context.Context, githubUser serverauth.GitHubUser, registration serverauth.DeviceRegistration) (identity serverauth.Identity, err error) {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return identity, fmt.Errorf("begin device registration: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback(context.WithoutCancel(ctx))
		}
	}()
	// Stable GitHub IDs provide a compact per-account serialization key and
	// avoid creating an orphan user during concurrent first logins.
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, githubUser.ID); err != nil {
		return identity, fmt.Errorf("lock GitHub identity: %w", err)
	}
	// The schema accepts only GitHub's avatar host. Dropping anything else
	// here keeps an unexpected URL from failing the login itself.
	githubUser.AvatarURL = serverauth.AvatarURL(githubUser.AvatarURL)
	var userID string
	err = tx.QueryRow(ctx, `SELECT user_id::text FROM github_identities WHERE github_user_id = $1`, githubUser.ID).Scan(&userID)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		userID = registration.UserID
		if _, err = tx.Exec(ctx, `INSERT INTO users(id, display_name) VALUES ($1::uuid, $2)`, userID, githubUser.Login); err != nil {
			return identity, fmt.Errorf("create tokenholic user: %w", err)
		}
		if _, err = tx.Exec(ctx, `INSERT INTO github_identities(github_user_id, user_id, login, avatar_url) VALUES ($1, $2::uuid, $3, NULLIF($4, ''))`, githubUser.ID, userID, githubUser.Login, githubUser.AvatarURL); err != nil {
			return identity, fmt.Errorf("create GitHub identity: %w", err)
		}
	case err != nil:
		return identity, fmt.Errorf("lookup GitHub identity: %w", err)
	default:
		// Each login refreshes the avatar, including clearing one GitHub no longer reports.
		if _, err = tx.Exec(ctx, `UPDATE github_identities SET login = $2, avatar_url = NULLIF($3, ''), updated_at = CURRENT_TIMESTAMP WHERE github_user_id = $1`, githubUser.ID, githubUser.Login, githubUser.AvatarURL); err != nil {
			return identity, fmt.Errorf("refresh GitHub identity: %w", err)
		}
		if _, err = tx.Exec(ctx, `UPDATE users SET display_name = $2, updated_at = CURRENT_TIMESTAMP WHERE id = $1::uuid`, userID, githubUser.Login); err != nil {
			return identity, fmt.Errorf("refresh tokenholic user: %w", err)
		}
	}
	deviceTag, err := tx.Exec(ctx, `
		INSERT INTO devices(id, user_id, client_version)
		VALUES ($1::uuid, $2::uuid, NULLIF($3, ''))
		ON CONFLICT (id) DO NOTHING
	`, registration.DeviceID, userID, registration.ClientVersion)
	if err != nil {
		return identity, fmt.Errorf("create device: %w", err)
	}
	if deviceTag.RowsAffected() == 0 {
		var existingUserID string
		if err = tx.QueryRow(ctx, `SELECT user_id::text FROM devices WHERE id = $1::uuid`, registration.DeviceID).Scan(&existingUserID); err != nil {
			return identity, fmt.Errorf("verify repeated device registration: %w", err)
		}
		if existingUserID != userID {
			return identity, errors.New("repeated device registration has different owner")
		}
	}
	credentialTag, err := tx.Exec(ctx, `
		INSERT INTO device_credentials(id, device_id, secret_hash, expires_at)
		VALUES ($1::uuid, $2::uuid, $3, $4)
		ON CONFLICT (id) DO NOTHING
	`, registration.Credential.CredentialID, registration.DeviceID, registration.Credential.SecretHash[:], registration.Credential.ExpiresAt)
	if err != nil {
		return identity, fmt.Errorf("create device credential: %w", err)
	}
	if credentialTag.RowsAffected() == 0 {
		var existingDeviceID string
		var existingHash []byte
		if err = tx.QueryRow(ctx, `SELECT device_id::text, secret_hash FROM device_credentials WHERE id = $1::uuid`, registration.Credential.CredentialID).Scan(&existingDeviceID, &existingHash); err != nil {
			return identity, fmt.Errorf("verify repeated credential registration: %w", err)
		}
		if existingDeviceID != registration.DeviceID || len(existingHash) != len(registration.Credential.SecretHash) || subtle.ConstantTimeCompare(existingHash, registration.Credential.SecretHash[:]) != 1 {
			return identity, errors.New("repeated credential registration differs")
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return identity, fmt.Errorf("commit device registration: %w", err)
	}
	return serverauth.Identity{UserID: userID, DeviceID: registration.DeviceID, Login: githubUser.Login}, nil
}

// Authenticate verifies a credential hash and records device freshness.
func (r *AuthRepository) Authenticate(ctx context.Context, proof serverauth.CredentialProof) (serverauth.Principal, error) {
	var storedHash []byte
	var principal serverauth.Principal
	err := r.pool.QueryRow(ctx, `
		SELECT c.secret_hash, d.user_id::text, d.id::text, c.id::text
		FROM device_credentials c
		JOIN devices d ON d.id = c.device_id
		JOIN users u ON u.id = d.user_id
		WHERE c.id = $1::uuid
		  AND c.revoked_at IS NULL
		  AND (c.expires_at IS NULL OR c.expires_at > CURRENT_TIMESTAMP)
		  AND d.revoked_at IS NULL
		  AND u.deleted_at IS NULL
	`, proof.CredentialID).Scan(&storedHash, &principal.UserID, &principal.DeviceID, &principal.CredentialID)
	if errors.Is(err, pgx.ErrNoRows) {
		return serverauth.Principal{}, serverauth.ErrUnauthorized
	}
	if err != nil {
		return serverauth.Principal{}, fmt.Errorf("authenticate device credential: %w", err)
	}
	if len(storedHash) != len(proof.SecretHash) || subtle.ConstantTimeCompare(storedHash, proof.SecretHash[:]) != 1 {
		return serverauth.Principal{}, serverauth.ErrUnauthorized
	}
	if _, err := r.pool.Exec(ctx, `UPDATE device_credentials SET last_used_at = CURRENT_TIMESTAMP WHERE id = $1::uuid`, principal.CredentialID); err != nil {
		return serverauth.Principal{}, fmt.Errorf("record credential authentication: %w", err)
	}
	if _, err := r.pool.Exec(ctx, `UPDATE devices SET last_seen_at = CURRENT_TIMESTAMP WHERE id = $1::uuid`, principal.DeviceID); err != nil {
		return serverauth.Principal{}, fmt.Errorf("record device authentication: %w", err)
	}
	return principal, nil
}

// RotateCredential atomically revokes the presented credential and stores its
// replacement for the same device.
func (r *AuthRepository) RotateCredential(ctx context.Context, proof serverauth.CredentialProof, replacement serverauth.NewCredential) (principal serverauth.Principal, err error) {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return principal, fmt.Errorf("begin credential rotation: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback(context.WithoutCancel(ctx))
		}
	}()
	var storedHash []byte
	err = tx.QueryRow(ctx, `
		SELECT c.secret_hash, d.user_id::text, d.id::text, c.id::text
		FROM device_credentials c
		JOIN devices d ON d.id = c.device_id
		JOIN users u ON u.id = d.user_id
		WHERE c.id = $1::uuid AND c.revoked_at IS NULL
		  AND (c.expires_at IS NULL OR c.expires_at > CURRENT_TIMESTAMP)
		  AND d.revoked_at IS NULL AND u.deleted_at IS NULL
		FOR UPDATE OF c
	`, proof.CredentialID).Scan(&storedHash, &principal.UserID, &principal.DeviceID, &principal.CredentialID)
	if errors.Is(err, pgx.ErrNoRows) {
		return principal, serverauth.ErrUnauthorized
	}
	if err != nil {
		return principal, fmt.Errorf("lock credential for rotation: %w", err)
	}
	if len(storedHash) != len(proof.SecretHash) || subtle.ConstantTimeCompare(storedHash, proof.SecretHash[:]) != 1 {
		return principal, serverauth.ErrUnauthorized
	}
	if _, err = tx.Exec(ctx, `UPDATE device_credentials SET revoked_at = CURRENT_TIMESTAMP WHERE id = $1::uuid`, proof.CredentialID); err != nil {
		return principal, fmt.Errorf("revoke rotated credential: %w", err)
	}
	if _, err = tx.Exec(ctx, `
		INSERT INTO device_credentials(id, device_id, secret_hash, expires_at)
		VALUES ($1::uuid, $2::uuid, $3, $4)
	`, replacement.CredentialID, principal.DeviceID, replacement.SecretHash[:], replacement.ExpiresAt); err != nil {
		return principal, fmt.Errorf("store rotated credential: %w", err)
	}
	if err = tx.Commit(ctx); err != nil {
		return principal, fmt.Errorf("commit credential rotation: %w", err)
	}
	principal.CredentialID = replacement.CredentialID
	return principal, nil
}

// RevokeDevice revokes the device and all current credentials atomically.
func (r *AuthRepository) RevokeDevice(ctx context.Context, proof serverauth.CredentialProof) (err error) {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin device revocation: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback(context.WithoutCancel(ctx))
		}
	}()
	var storedHash []byte
	var deviceID string
	err = tx.QueryRow(ctx, `
		SELECT c.secret_hash, c.device_id::text
		FROM device_credentials c
		JOIN devices d ON d.id = c.device_id
		WHERE c.id = $1::uuid AND c.revoked_at IS NULL AND d.revoked_at IS NULL
		FOR UPDATE OF c, d
	`, proof.CredentialID).Scan(&storedHash, &deviceID)
	if errors.Is(err, pgx.ErrNoRows) {
		return serverauth.ErrUnauthorized
	}
	if err != nil {
		return fmt.Errorf("lock device for revocation: %w", err)
	}
	if len(storedHash) != len(proof.SecretHash) || subtle.ConstantTimeCompare(storedHash, proof.SecretHash[:]) != 1 {
		return serverauth.ErrUnauthorized
	}
	if _, err = tx.Exec(ctx, `UPDATE devices SET revoked_at = CURRENT_TIMESTAMP WHERE id = $1::uuid`, deviceID); err != nil {
		return fmt.Errorf("revoke device: %w", err)
	}
	if _, err = tx.Exec(ctx, `UPDATE device_credentials SET revoked_at = COALESCE(revoked_at, CURRENT_TIMESTAMP) WHERE device_id = $1::uuid`, deviceID); err != nil {
		return fmt.Errorf("revoke device credentials: %w", err)
	}
	if err = tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit device revocation: %w", err)
	}
	return nil
}
