package adapters

import (
	"path/filepath"

	"github.com/ansonnchan/tokenholic/internal/normalize"
)

// Environment is everything default discovery reads about the user. Each
// platform consults only the variables its providers document; the README
// lists them with their sources.
type Environment struct {
	Home string
	// CodexHome is CODEX_HOME when the user set it. Empty selects the default.
	CodexHome string
	// Getenv looks up a platform directory variable such as XDG_CONFIG_HOME.
	// A nil lookup means every variable is unset.
	Getenv func(string) string
}

func (e Environment) lookup(name string) string {
	if e.Getenv == nil {
		return ""
	}
	return e.Getenv(name)
}

// MacRoots returns the coding-agent evidence roots supported on macOS.
// Ordinary desktop chat storage is intentionally absent.
func MacRoots(home, codexHome string) []RootSpec {
	roots := []RootSpec{claudeCLIRoot(filepath.Join(home, ".claude"), false)}
	roots = append(roots, claudeDesktopRoots(filepath.Join(home, "Library", "Application Support", "Claude"))...)
	// The default Codex home is shared by CLI and Desktop in current installs.
	return append(roots, codexHomes(home, codexHome, RootCodexDesktop, normalize.SurfaceDesktop)...)
}

// LinuxRoots returns the coding-agent evidence roots supported on Linux.
// configHome is XDG_CONFIG_HOME; anything but an absolute path selects the
// specification's default, ~/.config.
//
// Claude Code and Codex keep their data under the home directory on Linux
// and ignore the XDG variables. Two locations do follow XDG_CONFIG_HOME:
// Claude Desktop's data folder, and the directory Claude Code 1.0.28 to
// 1.0.30 briefly wrote sessions to, which is read when it still holds any.
func LinuxRoots(home, codexHome, configHome string) []RootSpec {
	if !filepath.IsAbs(configHome) {
		configHome = filepath.Join(home, ".config")
	}
	roots := []RootSpec{
		claudeCLIRoot(filepath.Join(home, ".claude"), false),
		claudeCLIRoot(filepath.Join(configHome, "claude"), true),
	}
	roots = append(roots, claudeDesktopRoots(filepath.Join(configHome, "Claude"))...)
	// Nothing first-party says the Linux desktop app writes to ~/.codex, so a
	// default home beside a custom CODEX_HOME stays unattributed.
	return append(roots, codexHomes(home, codexHome, RootCodexShared, normalize.SurfaceSharedLocal)...)
}

// claudeCLIRoot is the transcript directory inside one Claude Code
// configuration directory.
func claudeCLIRoot(configDirectory string, optional bool) RootSpec {
	return RootSpec{
		Kind: RootClaudeCLI, Source: normalize.SourceClaude, Surface: normalize.SurfaceCLI,
		Path: filepath.Join(configDirectory, "projects"), Format: "claude-jsonl-v1", Extensions: []string{".jsonl"},
		Capabilities: claudeCapabilities(), Optional: optional,
	}
}

// claudeDesktopRoots lists the coding evidence inside one Claude Desktop
// data folder: Code session records and the local Agent and Cowork
// subtrees. Nothing else in the folder is opened, and chat storage never is.
func claudeDesktopRoots(dataFolder string) []RootSpec {
	sessions := filepath.Join(dataFolder, "local-agent-mode-sessions")
	return []RootSpec{
		{
			Kind: RootClaudeDesktopCode, Source: normalize.SourceClaude, Surface: normalize.SurfaceDesktopCode,
			Path: filepath.Join(dataFolder, "claude-code-sessions"), Format: "claude-desktop-code", Extensions: []string{".json", ".jsonl"},
			Capabilities: claudeCapabilities(),
		},
		{
			Kind: RootClaudeDesktopAgent, Source: normalize.SourceClaude, Surface: normalize.SurfaceDesktopAgent,
			Path: sessions, Format: "claude-local-agent-jsonl", Extensions: []string{".jsonl"},
			Match: func(relative string) bool { return hasPathSegment(relative, "rpm") }, Capabilities: claudeCapabilities(),
		},
		{
			Kind: RootClaudeDesktopCowork, Source: normalize.SourceClaude, Surface: normalize.SurfaceDesktopCowork,
			Path: sessions, Format: "claude-cowork-audit-jsonl", Extensions: []string{".jsonl"},
			Match: func(relative string) bool {
				return filepath.Base(relative) == "audit.jsonl" && !hasPathSegment(relative, "rpm")
			}, Capabilities: claudeCapabilities(),
		},
	}
}

// codexHomes lists the Codex homes to read. Codex resolves its home the same
// way on every system: CODEX_HOME, or .codex under the home directory. With
// no override that one home serves every Codex surface and is reported as
// shared, which avoids double discovery and false attribution. With an
// override, the override is the CLI's and the default home is whatever
// defaultKind says this platform can prove about it.
func codexHomes(home, codexHome string, defaultKind RootKind, defaultSurface normalize.Surface) []RootSpec {
	defaultHome := filepath.Join(home, ".codex")
	if codexHome == "" || filepath.Clean(codexHome) == filepath.Clean(defaultHome) {
		return codexRoots(RootCodexShared, normalize.SurfaceSharedLocal, defaultHome)
	}
	return append(codexRoots(RootCodexCLI, normalize.SurfaceCLI, codexHome),
		codexRoots(defaultKind, defaultSurface, defaultHome)...)
}

// codexRoots lists where one Codex home keeps rollouts. Codex moves a rollout
// from sessions to archived_sessions when the user archives it, and the usage
// in it is no less real. A rollout present in both is counted once, because a
// request's identity does not depend on the file it was read from.
func codexRoots(kind RootKind, surface normalize.Surface, home string) []RootSpec {
	root := func(directory string, optional bool) RootSpec {
		return RootSpec{
			Kind: kind, Source: normalize.SourceCodex, Surface: surface,
			Path: filepath.Join(home, directory), Format: "codex-rollout-jsonl", Extensions: []string{".jsonl"},
			Capabilities: codexCapabilities(), Optional: optional,
		}
	}
	return []RootSpec{root("sessions", false), root("archived_sessions", true)}
}
