package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/ansonnchan/tokenholic/internal/account"
	"github.com/ansonnchan/tokenholic/internal/adapters"
	"github.com/ansonnchan/tokenholic/internal/application"
	"github.com/ansonnchan/tokenholic/internal/collect"
	"github.com/ansonnchan/tokenholic/internal/modelcatalog"
	"github.com/ansonnchan/tokenholic/internal/multiplayer"
	"github.com/ansonnchan/tokenholic/internal/preferences"
	"github.com/ansonnchan/tokenholic/internal/store"
	"github.com/ansonnchan/tokenholic/internal/tui"
	"github.com/ansonnchan/tokenholic/internal/tui/theme"
)

const localCloseTimeout = 5 * time.Second

// displayOptions are the presentation switches a user can set.
type displayOptions struct {
	noColor bool
	ascii   bool
	// mode is the look TOKENHOLIC_THEME names, when modeSet. It overrides the
	// saved choice for this run; nobody needs it, since t switches in the TUI.
	mode    theme.Mode
	modeSet bool
}

// displayFromEnvironment reads the presentation switches. Color is on unless
// NO_COLOR is set. TOKENHOLIC_THEME=light or dark starts in that look
// whatever was saved; any other value is ignored. TOKENHOLIC_ASCII, or a
// terminal that declares itself dumb, selects the ASCII glyph set.
func displayFromEnvironment(getenv func(string) string) displayOptions {
	options := displayOptions{
		noColor: getenv("NO_COLOR") != "",
		ascii:   getenv("TOKENHOLIC_ASCII") != "" || getenv("TERM") == "dumb",
	}
	options.mode, options.modeSet = theme.ParseMode(getenv("TOKENHOLIC_THEME"))
	return options
}

func launchLocalTUI(ctx context.Context, home, codexHome, apiURL string, input io.Reader, output io.Writer, display displayOptions) (err error) {
	database, service, err := openLocalApplication(ctx, home, codexHome)
	if err != nil {
		return err
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), localCloseTimeout)
		defer cancel()
		if closeErr := database.Close(closeCtx); err == nil && closeErr != nil {
			err = closeErr
		}
	}()
	config := tui.Config{NoColor: display.noColor, ASCII: display.ascii, OpenURL: openBrowser}
	// The look is the user's saved choice, light until they make one. A
	// preferences file that cannot be read is not worth failing a launch.
	if path, pathErr := preferences.DefaultPath(); pathErr == nil {
		saved, _ := preferences.Load(path)
		config.Mode, _ = theme.ParseMode(saved.Theme)
		config.SaveMode = func(mode theme.Mode) error {
			current, _ := preferences.Load(path)
			current.Theme = mode.String()
			return preferences.Save(path, current)
		}
	}
	if display.modeSet {
		config.Mode = display.mode
	}
	if apiURL != "" {
		client, clientErr := multiplayer.NewClient(apiURL, nil)
		if clientErr != nil {
			return clientErr
		}
		groups, clientErr := multiplayer.NewService(client, account.NewKeyringStore(apiURL), localTimezone())
		if clientErr != nil {
			return clientErr
		}
		// Assign only a real service: a nil *Service stored in the interface
		// would not compare equal to nil, and the board area would call it.
		config.Multiplayer = groups
		claim, clientErr := newDeviceAccount(apiURL, database)
		if clientErr != nil {
			return clientErr
		}
		config.Account = claim
	}
	return tui.Run(ctx, service, input, output, config)
}

func localTimezone() string {
	if configured := strings.TrimSpace(os.Getenv("TZ")); configured != "" && configured != "Local" {
		if _, err := time.LoadLocation(configured); err == nil {
			return configured
		}
	}
	if name := time.Local.String(); name != "" && name != "Local" {
		if _, err := time.LoadLocation(name); err == nil {
			return name
		}
	}
	if target, err := os.Readlink("/etc/localtime"); err == nil {
		if _, zone, found := strings.Cut(target, "zoneinfo/"); found && zone != "" {
			if _, err := time.LoadLocation(zone); err == nil {
				return zone
			}
		}
	}
	return "UTC"
}

// loadLocalUsage scans local records and returns the dashboard's totals, for
// commands that report usage without opening the TUI.
func loadLocalUsage(ctx context.Context, home, codexHome string) (dashboard application.Dashboard, err error) {
	database, service, err := openLocalApplication(ctx, home, codexHome)
	if err != nil {
		return application.Dashboard{}, err
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), localCloseTimeout)
		defer cancel()
		if closeErr := database.Close(closeCtx); err == nil && closeErr != nil {
			err = closeErr
		}
	}()
	if err := service.Refresh(ctx); err != nil {
		return application.Dashboard{}, err
	}
	return service.Load(ctx)
}

// defaultRoots lists this system's evidence roots. The platform directory
// variables come from the process environment: they locate provider data and
// are not settings of tokenholic's own.
func defaultRoots(home, codexHome string) []adapters.RootSpec {
	return adapters.DefaultRoots(adapters.Environment{Home: home, CodexHome: codexHome, Getenv: os.Getenv})
}

func openLocalApplication(ctx context.Context, home, codexHome string) (*store.DB, *application.Service, error) {
	path, err := store.DefaultPath()
	if err != nil {
		return nil, nil, err
	}
	database, err := store.Open(ctx, path)
	if err != nil {
		return nil, nil, err
	}
	// The bundled catalog only previews resolution and cost locally. The
	// server derives its own values and never sees this preview.
	previewer, err := modelcatalog.BundledDeriver()
	if err != nil {
		_ = database.Close(context.Background())
		return nil, nil, fmt.Errorf("initialize model catalog: %w", err)
	}
	orchestrator := collect.Orchestrator{
		Discovery: adapters.Discoverer{Roots: defaultRoots(home, codexHome)},
		Collector: collect.Collector{SourceVersion: "local-jsonl-v1"},
		Store:     database, AdapterVersion: "native-v1",
		// Agents without a native adapter are read through the pinned
		// ccusage, unless the user turned it off or this install has none.
		DayLevel: dayLevelReader(home, os.Getenv, preferences.DefaultPath),
	}
	service, err := application.NewService(database, orchestrator, previewer, time.Local, time.Now)
	if err != nil {
		_ = database.Close(context.Background())
		return nil, nil, err
	}
	return database, service, nil
}
