package doctor

import (
	"context"
	"fmt"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/ansonnchan/tokenholic/internal/adapters"
	"github.com/ansonnchan/tokenholic/internal/adapters/ccusage"
	"github.com/ansonnchan/tokenholic/internal/normalize"
	"github.com/ansonnchan/tokenholic/internal/store"
)

// DiscoveryProbe supplies source health without requiring usage collection.
type DiscoveryProbe interface {
	Discover(context.Context) []adapters.Finding
}

// ParserProbe checks compatibility of an optional external parser.
type ParserProbe interface {
	Check(context.Context) ParserStatus
}

// ParserStatus distinguishes absence, incompatibility, execution failure, and timeout.
type ParserStatus struct {
	Found      bool
	Compatible bool
	// Issue is empty, "timeout", or "execution_failed"; incompatibility is
	// represented by Found without Compatible so no external output is retained.
	Issue string
}

// Report is a path-redacted snapshot of platform, parser, and source health.
type Report struct {
	GOOS     string
	GOARCH   string
	Parser   ParserStatus
	Findings []adapters.Finding
	Database DatabaseStatus
	// API says where the API address comes from. It never holds the address:
	// an override can name a private host.
	API APISource
}

// APISource is where a command's API address comes from. The zero value
// leaves the line out of the report.
type APISource string

const (
	// APIBuiltIn means the release build's own hosted address is in use.
	APIBuiltIn APISource = "built_in"
	// APIOverridden means TOKENHOLIC_API_URL replaced it.
	APIOverridden APISource = "overridden"
	// APINone means a development build with no address and no override.
	APINone APISource = "none"
)

// Diagnose gathers discovery and parser health under the caller's context.
func Diagnose(ctx context.Context, discovery DiscoveryProbe, parser ParserProbe, database DatabaseProbe) Report {
	report := Report{GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, Findings: discovery.Discover(ctx)}
	if parser != nil {
		report.Parser = parser.Check(ctx)
	}
	if database != nil {
		report.Database = database.Check(ctx)
	}
	return report
}

// ExitCode returns 1 for actionable warnings and 2 for unusable storage.
// Missing optional sources do not make the diagnostic fail.
func (r Report) ExitCode() int {
	if r.Database.State != "" && r.Database.State != DatabaseReady && r.Database.State != DatabaseNotInitialized {
		return 2
	}
	if !platformSupported(r.GOOS) {
		return 1
	}
	if r.Parser.Issue != "" || r.Parser.Found && !r.Parser.Compatible {
		return 1
	}
	for _, finding := range r.Findings {
		switch finding.Status {
		case normalize.HealthPermissionDenied, normalize.HealthUnsupported, normalize.HealthDegraded, normalize.HealthError:
			return 1
		}
	}
	if r.Database.State == DatabaseNotInitialized || r.Database.Stale {
		return 1
	}
	for _, health := range r.Database.AdapterHealth {
		switch health.Health.Status {
		case normalize.HealthPermissionDenied, normalize.HealthUnsupported, normalize.HealthDegraded, normalize.HealthError:
			return 1
		}
	}
	return 0
}

// Render formats a deterministic report using only redaction-safe finding fields.
func (r Report) Render() string {
	var output strings.Builder
	fmt.Fprintln(&output, "tokenholic doctor")
	if platformSupported(r.GOOS) {
		fmt.Fprintf(&output, "Platform: %s/%s\n", r.GOOS, r.GOARCH)
	} else {
		fmt.Fprintf(&output, "Platform: %s/%s (not supported; no coding-agent locations are known here)\n", r.GOOS, r.GOARCH)
	}
	switch r.API {
	case APIBuiltIn:
		fmt.Fprintln(&output, "API address: built in; no setting needed")
	case APIOverridden:
		fmt.Fprintln(&output, "API address: set by TOKENHOLIC_API_URL (address not shown)")
	case APINone:
		fmt.Fprintln(&output, "API address: none; this is a development build, so account commands need TOKENHOLIC_API_URL")
	}
	switch {
	case r.Parser.Compatible:
		fmt.Fprintf(&output, "ccusage: compatible (%s)\n", ccusage.PinnedVersion)
	case r.Parser.Issue == "timeout":
		fmt.Fprintln(&output, "ccusage: version check timed out")
	case r.Parser.Issue == "execution_failed":
		fmt.Fprintln(&output, "ccusage: found but could not execute")
	case r.Parser.Found:
		fmt.Fprintf(&output, "ccusage: unsupported (requires %s)\n", ccusage.PinnedVersion)
	default:
		fmt.Fprintf(&output, "ccusage: not found (optional pinned parser %s)\n", ccusage.PinnedVersion)
	}
	fmt.Fprintln(&output, "Sources:")

	findings := append([]adapters.Finding(nil), r.Findings...)
	sort.SliceStable(findings, func(i, j int) bool { return sourceOrder(findings[i].Kind) < sourceOrder(findings[j].Kind) })
	for _, finding := range findings {
		// Candidate paths are intentionally never consulted here. Finding metadata
		// is sufficient to diagnose a source without exposing a user directory.
		fmt.Fprintf(&output, "  %-29s %s", label(finding.Kind), statusText(finding))
		if finding.SharedRoot {
			fmt.Fprint(&output, "; shared records deduplicated")
		}
		if finding.Format != "" {
			fmt.Fprintf(&output, "; format %s", finding.Format)
		}
		if finding.Capabilities.Model != "" {
			fmt.Fprintf(&output, "; model %s; cache-write %s", finding.Capabilities.Model, finding.Capabilities.InputCacheWrite)
		}
		fmt.Fprintln(&output)
		for _, warning := range finding.Warnings {
			fmt.Fprintf(&output, "    warning: %s\n", warningText(warning.Code))
		}
	}
	if r.Database.State != "" {
		r.renderDatabase(&output)
	}
	if r.noSupportedSources() {
		fmt.Fprintln(&output, "Source summary: no supported coding-agent usage found")
		fmt.Fprintln(&output, "Next: install or use a supported coding agent, then refresh tokenholic")
	}
	if r.ExitCode() == 0 {
		fmt.Fprintln(&output, "Result: ready; missing optional sources are not errors")
	} else {
		fmt.Fprintln(&output, "Result: issues found; review the warnings above")
	}
	return output.String()
}

// platformSupported reports whether default discovery knows any location on
// an operating system. It must agree with adapters.DefaultRoots.
func platformSupported(goos string) bool {
	return goos == "darwin" || goos == "linux" || goos == "windows"
}

func (r Report) noSupportedSources() bool {
	if len(r.Findings) == 0 {
		return false
	}
	for _, finding := range r.Findings {
		if finding.Status != normalize.HealthNotFound {
			return false
		}
	}
	return true
}

func (r Report) renderDatabase(output *strings.Builder) {
	database := r.Database
	fmt.Fprintln(output, "Database:")
	fmt.Fprintf(output, "  Location: %s (path redacted)\n", database.Location)
	switch database.State {
	case DatabaseReady:
		fmt.Fprintln(output, "  Open: ok")
		fmt.Fprintln(output, "  Integrity: ok")
		fmt.Fprintf(output, "  Schema: %d (compatible; supported %d)\n", database.SchemaVersion, database.SupportedVersion)
	case DatabaseNotInitialized:
		fmt.Fprintln(output, "  Open: not initialized; run `tokenholic` to create local storage")
	case DatabaseLocked:
		fmt.Fprintln(output, "  Open: locked; close other tokenholic processes and retry")
	case DatabaseCorrupt:
		fmt.Fprintln(output, "  Integrity: failed; restore or replace the local database")
	case DatabaseIncompatible:
		fmt.Fprintf(output, "  Schema: incompatible (supported %d); update tokenholic before retrying\n", database.SupportedVersion)
	default:
		fmt.Fprintln(output, "  Open: failed; check application-data permissions and retry")
	}
	if database.State != DatabaseReady {
		return
	}
	if database.LastSuccessfulCollection == nil {
		fmt.Fprintln(output, "  Last collection: never; run a local refresh")
	} else if database.Stale {
		fmt.Fprintf(output, "  Last collection: %s (stale; run a local refresh)\n", database.LastSuccessfulCollection.UTC().Format(time.RFC3339))
	} else {
		fmt.Fprintf(output, "  Last collection: %s\n", database.LastSuccessfulCollection.UTC().Format(time.RFC3339))
	}
	fmt.Fprintf(output, "  Source checkpoints: %d\n", database.CheckpointCount)
	health := append([]store.StoredAdapterHealth(nil), database.AdapterHealth...)
	sort.Slice(health, func(i, j int) bool {
		if health[i].Health.Source != health[j].Health.Source {
			return health[i].Health.Source < health[j].Health.Source
		}
		return health[i].Health.Surface < health[j].Health.Surface
	})
	for _, record := range health {
		fmt.Fprintf(output, "  Adapter %s/%s: %s\n", record.Health.Source, record.Health.Surface, record.Health.Status)
	}
	fmt.Fprintf(output, "  Pending local outbox: %d\n", database.PendingOutbox)
}

func statusText(finding adapters.Finding) string {
	switch finding.Status {
	case normalize.HealthDetected:
		return fmt.Sprintf("detected (%d candidate files)", finding.RecordCount)
	case normalize.HealthNotFound:
		return "not found"
	case normalize.HealthPermissionDenied:
		return "permission denied"
	case normalize.HealthUnsupported:
		return "unsupported layout"
	case normalize.HealthDegraded:
		return fmt.Sprintf("degraded (%d candidate files)", finding.RecordCount)
	default:
		return "error"
	}
}

func label(kind adapters.RootKind) string {
	switch kind {
	case adapters.RootClaudeCLI:
		return "Claude Code CLI"
	case adapters.RootClaudeDesktopCode:
		return "Claude Desktop Code"
	case adapters.RootClaudeDesktopAgent:
		return "Claude Desktop Agent"
	case adapters.RootClaudeDesktopCowork:
		return "Claude Desktop Cowork"
	case adapters.RootCodexCLI:
		return "Codex CLI"
	case adapters.RootCodexDesktop:
		return "Codex Desktop"
	case adapters.RootCodexShared:
		return "Codex CLI/Desktop shared"
	default:
		return "Unknown source"
	}
}

func sourceOrder(kind adapters.RootKind) int {
	switch kind {
	case adapters.RootClaudeCLI:
		return 0
	case adapters.RootClaudeDesktopCode:
		return 1
	case adapters.RootClaudeDesktopAgent:
		return 2
	case adapters.RootClaudeDesktopCowork:
		return 3
	case adapters.RootCodexCLI:
		return 5
	case adapters.RootCodexDesktop:
		return 6
	case adapters.RootCodexShared:
		return 4
	default:
		return 7
	}
}

func warningText(code string) string {
	switch code {
	case "source_root_unreadable", "source_entry_permission_denied":
		return "the source exists but cannot be read; check its file permissions"
	case "source_root_symlink":
		return "the source root is a symlink; configure the resolved data root explicitly"
	case "source_root_not_directory":
		return "the expected source root is not a directory"
	case "discovery_canceled":
		return "source discovery was canceled"
	case "no_candidate_records":
		return "the source root exists but has no candidate coding records"
	case "surface_attribution_unavailable":
		return "CLI and Desktop share this record; its originating surface is unavailable"
	default:
		return "source discovery could not complete"
	}
}
