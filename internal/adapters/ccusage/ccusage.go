package ccusage

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
)

const (
	// PinnedVersion is the only ccusage release tokenholic executes.
	PinnedVersion = "20.0.51"
	// SchemaID versions the pinned JSON contract and how it is run.
	SchemaID = "daily-agent-contract-v2"
)

// Agent names one coding agent ccusage reports on. Its value is the
// subcommand ccusage expects.
type Agent string

const (
	// AgentCopilot is GitHub Copilot CLI.
	AgentCopilot Agent = "copilot"
	// AgentGemini is Gemini CLI.
	AgentGemini Agent = "gemini"
	// AgentGrok is Grok Build CLI.
	AgentGrok Agent = "grok"
	// AgentOpenCode is OpenCode.
	AgentOpenCode Agent = "opencode"
	// AgentQwen is Qwen Code.
	AgentQwen Agent = "qwen"
)

// Agents lists every agent the pinned contract covers, in a fixed order.
// Claude and Codex are absent on purpose: they have native adapters, and
// reading them a second way would count the same usage twice.
func Agents() []Agent {
	return []Agent{AgentCopilot, AgentGemini, AgentGrok, AgentOpenCode, AgentQwen}
}

// reportArguments follow the agent name on every run. They ask for one row
// per UTC day as JSON, without ccusage's own cost figures, and forbid it to
// fetch prices.
func reportArguments() []string {
	return []string{"daily", "--json", "--no-cost", "--offline", "--timezone", "UTC"}
}

// Arguments returns the complete argument array for one agent's report.
func Arguments(agent Agent) []string {
	return append([]string{string(agent)}, reportArguments()...)
}

// pinnedExecutables holds the SHA-256 of the ccusage executable inside each
// platform package of the pinned release, keyed by the package's platform
// name. A file with any other digest is never executed.
var pinnedExecutables = map[string]string{
	"darwin-arm64": "81a9af33657b5bf6247975ae8e5969ac71367c5a579fd4edeb946dc03ac0aa02",
	"darwin-x64":   "180eab92960d2b880c38278040b67b2446c46d1e41cff62851437341c68e75ef",
	"linux-arm64":  "f5d67e3e03ac894fd1241642624e3c16b6e30b68f21dcf43471df3d62c536f59",
	"linux-x64":    "7ddd309d8f4d1c2c007647f43c300a8261acbe9e5cb355e70cc428f0f419f8d2",
	"win32-arm64":  "a581662fac8da3b769bfeb746b2b2522cdfb8d5690ca0f290862cc615fea48a3",
	"win32-x64":    "4a1e0f9c47b279fbb47a8637e2811f6cff6a48040e803a799a0a40e492886efa",
}

// platformName converts Go's operating system and architecture names to the
// ones ccusage's packages use. It returns "" where ccusage ships nothing.
func platformName(goos, goarch string) string {
	system := map[string]string{"darwin": "darwin", "linux": "linux", "windows": "win32"}[goos]
	processor := map[string]string{"arm64": "arm64", "amd64": "x64"}[goarch]
	if system == "" || processor == "" {
		return ""
	}
	return system + "-" + processor
}

// PinnedDigest returns the expected SHA-256 of the ccusage executable for a
// Go platform, and false where the pinned release has no build.
func PinnedDigest(goos, goarch string) (string, bool) {
	digest, found := pinnedExecutables[platformName(goos, goarch)]
	return digest, found
}

type lockPlatform struct {
	Package   string `json:"package"`
	Integrity string `json:"integrity"`
	SHA256    string `json:"sha256"`
}

type lockManifest struct {
	Tool      string                  `json:"tool"`
	Version   string                  `json:"version"`
	Schema    string                  `json:"schema"`
	Arguments []string                `json:"arguments"`
	Agents    []string                `json:"agents"`
	Platforms map[string]lockPlatform `json:"platforms"`
}

// ValidateLock checks that the manifest shipped with a release describes
// exactly what this build runs: the version, the arguments, the agents, and
// the executable digest of every platform. The npm integrity of each package
// is not compiled in; it is required to be present and well formed, and the
// packaging tests hold the npm manifest and lockfile to it.
func ValidateLock(data []byte) error {
	var lock lockManifest
	if err := strictDecode(data, &lock); err != nil {
		return fmt.Errorf("decode lock manifest: %w", err)
	}
	agents := make([]string, 0, len(Agents()))
	for _, agent := range Agents() {
		agents = append(agents, string(agent))
	}
	if lock.Tool != "ccusage" || lock.Version != PinnedVersion || lock.Schema != SchemaID ||
		!slices.Equal(lock.Arguments, reportArguments()) || !slices.Equal(lock.Agents, agents) ||
		len(lock.Platforms) != len(pinnedExecutables) {
		return errors.New("lock manifest does not match the compiled ccusage contract")
	}
	for name, digest := range pinnedExecutables {
		platform, found := lock.Platforms[name]
		if !found || platform.SHA256 != digest || platform.Package != "@ccusage/ccusage-"+name ||
			!strings.HasPrefix(platform.Integrity, "sha512-") || len(platform.Integrity) != len("sha512-")+88 {
			return fmt.Errorf("lock manifest does not match the compiled ccusage contract for %s", name)
		}
	}
	return nil
}

// rejectDuplicateKeys walks one JSON value and fails on an object that names
// a key twice. encoding/json keeps the last one silently, which would let a
// document say two things at once.
func rejectDuplicateKeys(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		keys := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("object key is not a string")
			}
			if _, duplicate := keys[key]; duplicate {
				return fmt.Errorf("duplicate JSON key %q", key)
			}
			keys[key] = struct{}{}
			if err := rejectDuplicateKeys(decoder); err != nil {
				return err
			}
		}
	case '[':
		for decoder.More() {
			if err := rejectDuplicateKeys(decoder); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("unexpected JSON delimiter %q", delimiter)
	}
	_, err = decoder.Token()
	return err
}

// Label returns the agent's product name for diagnostics and the TUI.
func (a Agent) Label() string {
	switch a {
	case AgentCopilot:
		return "GitHub Copilot CLI"
	case AgentGemini:
		return "Gemini CLI"
	case AgentGrok:
		return "Grok Build CLI"
	case AgentOpenCode:
		return "OpenCode"
	case AgentQwen:
		return "Qwen Code"
	default:
		return "Unknown agent"
	}
}
