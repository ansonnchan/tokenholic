package adapters

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/ansonnchan/tokenholic/internal/normalize"
)

// RootKind names one supported provider storage layout.
type RootKind string

const (
	// RootClaudeCLI is the Claude Code CLI project-session root.
	RootClaudeCLI RootKind = "claude_cli"
	// RootClaudeDesktopCode is the Claude Desktop Code-session root.
	RootClaudeDesktopCode RootKind = "claude_desktop_code"
	// RootClaudeDesktopAgent is the Claude Desktop local Agent subtree.
	RootClaudeDesktopAgent RootKind = "claude_desktop_agent"
	// RootClaudeDesktopCowork is the Claude Desktop Cowork audit subtree.
	RootClaudeDesktopCowork RootKind = "claude_desktop_cowork"
	// RootCodexCLI is a Codex home distinct from default desktop storage.
	RootCodexCLI RootKind = "codex_cli"
	// RootCodexDesktop is the default Codex Desktop session root.
	RootCodexDesktop RootKind = "codex_desktop"
	// RootCodexShared is storage that cannot be attributed to CLI or Desktop.
	RootCodexShared RootKind = "codex_shared"
)

// RootSpec defines a narrow read-only search without exposing its path publicly.
type RootSpec struct {
	Kind         RootKind
	Source       normalize.Source
	Surface      normalize.Surface
	Path         string
	Format       string
	Extensions   []string
	Match        func(string) bool
	Capabilities normalize.Capabilities
	// Optional marks a secondary location of a source whose primary root is
	// listed separately. It contributes records when it has any; when it is
	// absent or empty it reports nothing, so the source's health is decided
	// by its primary root alone. An unreadable optional root is still reported.
	Optional bool
}

// Candidate is a local file that matches one supported root contract.
// Path and RelativePath must never be serialized into diagnostics or uploads.
type Candidate struct {
	Source       normalize.Source
	Surface      normalize.Surface
	Kind         RootKind
	Path         string
	RelativePath string
}

// Finding records discovery health and private candidates for one root.
type Finding struct {
	Kind         RootKind
	Source       normalize.Source
	Surface      normalize.Surface
	Status       normalize.HealthStatus
	Format       string
	RecordCount  uint64
	Candidates   []Candidate
	Warnings     []normalize.Warning
	SharedRoot   bool
	Capabilities normalize.Capabilities
}

// Discoverer scans an explicit set of source roots.
type Discoverer struct {
	Roots []RootSpec
}

// Discover returns one finding per configured root and marks cross-root overlap.
func (d Discoverer) Discover(ctx context.Context) []Finding {
	findings := make([]Finding, 0, len(d.Roots))
	for _, root := range d.Roots {
		finding := discoverRoot(ctx, root)
		if root.Optional && len(finding.Candidates) == 0 &&
			(finding.Status == normalize.HealthNotFound || finding.Status == normalize.HealthDegraded) {
			continue
		}
		findings = append(findings, finding)
	}
	markSharedCandidates(findings)
	return findings
}

func discoverRoot(ctx context.Context, root RootSpec) Finding {
	finding := Finding{
		Kind: root.Kind, Source: root.Source, Surface: root.Surface, Format: root.Format,
		Capabilities: root.Capabilities,
	}
	info, err := os.Lstat(root.Path)
	if err != nil {
		finding.Status = statusForError(err)
		if finding.Status != normalize.HealthNotFound {
			finding.Warnings = []normalize.Warning{{Code: "source_root_unreadable"}}
		}
		return finding
	}
	if info.Mode()&os.ModeSymlink != 0 {
		// Refusing a root symlink prevents discovery from escaping the explicitly
		// supported provider directory into unrelated user data.
		finding.Status = normalize.HealthUnsupported
		finding.Warnings = []normalize.Warning{{Code: "source_root_symlink"}}
		return finding
	}
	if !info.IsDir() {
		finding.Status = normalize.HealthUnsupported
		finding.Warnings = []normalize.Warning{{Code: "source_root_not_directory"}}
		return finding
	}

	finding.Status = normalize.HealthDetected
	err = filepath.WalkDir(root.Path, func(path string, entry fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			if errors.Is(walkErr, fs.ErrPermission) {
				finding.Status = normalize.HealthPermissionDenied
				finding.Warnings = append(finding.Warnings, normalize.Warning{Code: "source_entry_permission_denied"})
				if entry != nil && entry.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			return walkErr
		}
		if entry.IsDir() {
			if path != root.Path && entry.Type()&os.ModeSymlink != 0 {
				return fs.SkipDir
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 || !entry.Type().IsRegular() {
			return nil
		}
		relative, err := filepath.Rel(root.Path, path)
		if err != nil {
			return fmt.Errorf("derive source-relative record identity: %w", err)
		}
		if !matchesExtension(path, root.Extensions) || (root.Match != nil && !root.Match(relative)) {
			return nil
		}
		finding.Candidates = append(finding.Candidates, Candidate{
			Source: root.Source, Surface: root.Surface, Kind: root.Kind,
			Path: path, RelativePath: relative,
		})
		finding.RecordCount++
		return nil
	})
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			finding.Status = normalize.HealthError
			finding.Warnings = append(finding.Warnings, normalize.Warning{Code: "discovery_canceled"})
			return finding
		}
		finding.Status = statusForError(err)
		finding.Warnings = append(finding.Warnings, normalize.Warning{Code: "source_scan_failed"})
	}
	if finding.Status == normalize.HealthDetected && finding.RecordCount == 0 {
		finding.Status = normalize.HealthDegraded
		finding.Warnings = append(finding.Warnings, normalize.Warning{Code: "no_candidate_records"})
	}
	return finding
}

func markSharedCandidates(findings []Finding) {
	// The same physical candidate under multiple roots cannot support a precise
	// surface claim, even though its provider remains known.
	owners := make(map[string][]int)
	for index, finding := range findings {
		for _, candidate := range finding.Candidates {
			owners[filepath.Clean(candidate.Path)] = append(owners[filepath.Clean(candidate.Path)], index)
		}
	}
	for _, indexes := range owners {
		if len(indexes) < 2 {
			continue
		}
		for _, index := range indexes {
			findings[index].SharedRoot = true
			if findings[index].Source == normalize.SourceCodex && !hasWarningCode(findings[index].Warnings, "surface_attribution_unavailable") {
				findings[index].Warnings = append(findings[index].Warnings, normalize.Warning{Code: "surface_attribution_unavailable"})
			}
		}
	}
}

func hasWarningCode(warnings []normalize.Warning, code string) bool {
	for _, warning := range warnings {
		if warning.Code == code {
			return true
		}
	}
	return false
}

func matchesExtension(path string, allowed []string) bool {
	for _, extension := range allowed {
		if strings.EqualFold(filepath.Ext(path), extension) {
			return true
		}
	}
	return false
}

func hasPathSegment(path, segment string) bool {
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		if part == segment {
			return true
		}
	}
	return false
}

func statusForError(err error) normalize.HealthStatus {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return normalize.HealthNotFound
	case errors.Is(err, fs.ErrPermission):
		return normalize.HealthPermissionDenied
	default:
		return normalize.HealthError
	}
}

func claudeCapabilities() normalize.Capabilities {
	return normalize.Capabilities{
		Model: normalize.CapabilityReported, InputUncached: normalize.CapabilityReported,
		InputCacheRead: normalize.CapabilityReported, InputCacheWrite: normalize.CapabilityReported,
		Output: normalize.CapabilityReported, ReasoningOutput: normalize.CapabilityUnsupported,
	}
}

func codexCapabilities() normalize.Capabilities {
	return normalize.Capabilities{
		Model: normalize.CapabilityInferred, InputUncached: normalize.CapabilityDerived,
		InputCacheRead: normalize.CapabilityReported, InputCacheWrite: normalize.CapabilityUnsupported,
		Output: normalize.CapabilityReported, ReasoningOutput: normalize.CapabilityReported,
	}
}
