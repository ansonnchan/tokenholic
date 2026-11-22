package account

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/zalando/go-keyring"
)

const keyringService = "io.tokenholic.cli"

// ErrNoSession means no tokenholic session is stored for this API origin.
var ErrNoSession = errors.New("not signed in")

// ErrVaultUnavailable means the operating system's credential store could
// not be used. There is no plaintext fallback, so nothing can be signed in or
// uploaded until the store works; VaultHelp says what to do about it.
var ErrVaultUnavailable = errors.New("the OS credential store is not available")

// KeyringStore keeps device credentials in the operating-system credential
// vault and never falls back to a plaintext configuration file.
type KeyringStore struct {
	account string
}

// NewKeyringStore isolates credentials by API origin.
func NewKeyringStore(apiURL string) *KeyringStore {
	digest := sha256.Sum256([]byte(apiURL))
	return &KeyringStore{account: "device-" + hex.EncodeToString(digest[:8])}
}

// Load reads the current session from the OS credential vault.
func (s *KeyringStore) Load() (Session, error) {
	encoded, err := keyring.Get(keyringService, s.account)
	if errors.Is(err, keyring.ErrNotFound) {
		return Session{}, ErrNoSession
	}
	if err != nil {
		return Session{}, fmt.Errorf("read OS credential vault: %w", unavailable(err))
	}
	var session Session
	if err := json.Unmarshal([]byte(encoded), &session); err != nil || session.Credential == "" {
		return Session{}, errors.New("stored device credential is invalid")
	}
	return session, nil
}

// Save replaces the current session in the OS credential vault.
func (s *KeyringStore) Save(session Session) error {
	if session.Credential == "" {
		return errors.New("device credential is empty")
	}
	encoded, err := json.Marshal(session)
	if err != nil {
		return fmt.Errorf("encode device credential: %w", err)
	}
	if err := keyring.Set(keyringService, s.account, string(encoded)); err != nil {
		return fmt.Errorf("write OS credential vault: %w", unavailable(err))
	}
	return nil
}

// Delete removes the current session from the OS credential vault.
func (s *KeyringStore) Delete() error {
	if err := keyring.Delete(keyringService, s.account); err != nil && !errors.Is(err, keyring.ErrNotFound) {
		return fmt.Errorf("delete OS credential vault entry: %w", unavailable(err))
	}
	return nil
}

// unavailable marks a vault failure so callers can tell it from a missing
// session or a network error without matching platform-specific text.
func unavailable(err error) error {
	return errors.Join(ErrVaultUnavailable, err)
}
