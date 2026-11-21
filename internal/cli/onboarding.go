package cli

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os/exec"
	"runtime"
	"time"

	"github.com/ansonnchan/tokenholic/internal/account"
	"github.com/ansonnchan/tokenholic/internal/autosync"
	"github.com/ansonnchan/tokenholic/internal/store"
	usageSync "github.com/ansonnchan/tokenholic/internal/sync"
	"github.com/ansonnchan/tokenholic/internal/tui"
)

// deviceAccount lets the TUI claim an account: it signs this device in and
// uploads the usage already committed to the open database. It is the same
// login and upload the claim command runs, without the terminal output.
type deviceAccount struct {
	apiURL   string
	manager  *account.Manager
	sessions *account.KeyringStore
	database *store.DB
}

func newDeviceAccount(apiURL string, database *store.DB) (*deviceAccount, error) {
	client, err := account.NewClient(apiURL, nil)
	if err != nil {
		return nil, err
	}
	sessions := account.NewKeyringStore(apiURL)
	manager, err := account.NewManager(client, sessions)
	if err != nil {
		return nil, err
	}
	return &deviceAccount{apiURL: apiURL, manager: manager, sessions: sessions, database: database}, nil
}

// Login returns the signed-in GitHub login, or "" when this device has no
// session.
func (a *deviceAccount) Login(context.Context) (string, error) {
	session, err := a.sessions.Load()
	if errors.Is(err, account.ErrNoSession) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("load device credential: %w", err)
	}
	return session.Login, nil
}

// Begin starts GitHub device authorization.
func (a *deviceAccount) Begin(ctx context.Context) (account.DeviceStart, error) {
	return a.manager.Begin(ctx, version)
}

// Complete waits for authorization and stores the device session.
func (a *deviceAccount) Complete(ctx context.Context, started account.DeviceStart) (string, error) {
	session, err := a.manager.Complete(ctx, started)
	if err != nil {
		return "", err
	}
	return session.Login, nil
}

// Sync uploads pending buckets under the lock the scheduled sync uses, so
// the two never upload at once.
func (a *deviceAccount) Sync(ctx context.Context) (uploaded int64, err error) {
	lock, err := autosync.AcquireDefault()
	if errors.Is(err, autosync.ErrAlreadyRunning) {
		return 0, tui.ErrSyncBusy
	}
	if err != nil {
		return 0, err
	}
	defer func() {
		if closeErr := lock.Close(); err == nil && closeErr != nil {
			err = closeErr
		}
	}()
	summary, err := uploadPending(ctx, a.database, a.apiURL)
	if errors.Is(err, usageSync.ErrUpgradeRequired) {
		return 0, tui.ErrUpgradeRequired
	}
	if err != nil {
		return 0, err
	}
	return summary.Acknowledged, nil
}

// uploadPending sends the outbox with this device's credential, rotating a
// credential that is close to expiry first.
func uploadPending(ctx context.Context, database *store.DB, apiURL string) (usageSync.Summary, error) {
	credentialStore := account.NewKeyringStore(apiURL)
	session, err := credentialStore.Load()
	if err != nil {
		return usageSync.Summary{}, err
	}
	if session.ExpiresAt.Before(time.Now().UTC().Add(credentialRefreshWindow)) {
		authClient, err := account.NewClient(apiURL, nil)
		if err != nil {
			return usageSync.Summary{}, err
		}
		manager, err := account.NewManager(authClient, credentialStore)
		if err != nil {
			return usageSync.Summary{}, err
		}
		if err := manager.Refresh(ctx); err != nil {
			return usageSync.Summary{}, err
		}
		session, err = credentialStore.Load()
		if err != nil {
			return usageSync.Summary{}, err
		}
	}
	client, err := usageSync.NewClient(apiURL, nil)
	if err != nil {
		return usageSync.Summary{}, err
	}
	return client.Run(ctx, database, session.Credential)
}

// openBrowser shows a link in the user's default browser. It opens HTTPS
// links only: the links it is given are GitHub's device page and a share
// draft, and nothing else should ever be handed to the system opener.
func openBrowser(link string) error {
	parsed, err := url.Parse(link)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return errors.New("refusing to open a link that is not HTTPS")
	}
	var command *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		command = exec.Command("open", parsed.String())
	case "windows":
		command = exec.Command("rundll32", "url.dll,FileProtocolHandler", parsed.String())
	default:
		command = exec.Command("xdg-open", parsed.String())
	}
	if err := command.Start(); err != nil {
		return fmt.Errorf("start browser: %w", err)
	}
	// Reap the opener without waiting on it.
	go func() { _ = command.Wait() }()
	return nil
}
