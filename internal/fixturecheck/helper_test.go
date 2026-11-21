package fixturecheck

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Key checks are normalized across camelCase, snake_case, and kebab-case so a
// fixture cannot bypass the privacy policy through source-specific spelling.
var prohibitedKeys = normalizedSet(
	"prompt", "response", "content", "text", "body", "messages", "transcript", "data",
	"source_code", "diff", "tool_input", "tool_output", "command", "path", "project_path", "cwd", "project",
	"repository", "repo", "branch", "api_key", "access_token", "authorization", "credential", "password", "secret",
)

// The allowlist is intentionally structural. Add a source field only after
// confirming that it cannot contain transcript or identifying material.
var allowedKeys = normalizedSet(
	"type", "sessionId", "requestId", "timestamp", "message", "id", "model", "usage",
	"input_tokens", "cached_input_tokens", "cache_read_input_tokens", "cache_creation_input_tokens",
	"cache_write_input_tokens", "output_tokens", "reasoning_output_tokens", "synthetic_broken",
	"cache_creation", "ephemeral_5m_input_tokens", "ephemeral_1h_input_tokens", "isApiErrorMessage",
	"payload", "session_id", "info", "total_token_usage", "last_token_usage", "turn_id",
	"daily", "date", "inputTokens", "outputTokens", "cacheCreationTokens", "cacheReadTokens",
	"reasoningOutputTokens", "totalTokens", "modelsUsed", "modelBreakdowns", "modelName",
	"cacheCreation1hTokens", "missingPricing", "totals", "unpricedModels", "models", "isFallback",
	// Pricing evidence. thread_settings is allowed only as a container: its
	// other members name the workspace and stay off this list.
	"service_tier", "speed", "thread_settings",
)

var rawKeyPattern = regexp.MustCompile(`"([A-Za-z][A-Za-z0-9_-]*)"[[:space:]]*:`)

func normalizedSet(values ...string) map[string]struct{} {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		result[normalizeKey(value)] = struct{}{}
	}
	return result
}

// Raw patterns protect malformed and non-JSON fixtures that structural decoding
// cannot inspect reliably.
var prohibitedPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)/Users/|/home/|/root/|~/|[A-Z]:\\`),
	regexp.MustCompile(`(?i)[a-z0-9._%+\-]+@[a-z0-9.\-]+\.[a-z]{2,}`),
	regexp.MustCompile(`(?i)https?://[^\s\"]*(github|gitlab|bitbucket)`),
	regexp.MustCompile(`(?i)git@[a-z0-9.\-]+:[^\s\"]+`),
	regexp.MustCompile(`(?i)(sk-|ghp_|github_pat_|xox[baprs]-|bearer[[:space:]]|AKIA)[a-z0-9_\-]{6,}`),
	regexp.MustCompile(`eyJ[a-zA-Z0-9_-]{8,}\.[a-zA-Z0-9_-]{8,}\.[a-zA-Z0-9_-]{8,}`),
}

func checkDir(root string) error {
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read fixture %s: %w", filepath.Base(path), err)
		}
		// Raw validation always runs first, including for fixtures intentionally
		// malformed to test parser recovery.
		if err := checkRaw(data); err != nil {
			return fmt.Errorf("fixture %s: %w", filepath.Base(path), err)
		}
		extension := strings.ToLower(filepath.Ext(path))
		if extension != ".json" && extension != ".jsonl" {
			return nil
		}
		if extension == ".jsonl" {
			return checkJSONL(path, data)
		}
		return checkJSON(path, data)
	})
}

func checkRaw(data []byte) error {
	for _, pattern := range prohibitedPatterns {
		if pattern.Match(data) {
			return fmt.Errorf("contains prohibited private material matching %q", pattern.String())
		}
	}
	for _, match := range rawKeyPattern.FindAllSubmatch(data, -1) {
		key := normalizeKey(string(match[1]))
		if _, found := prohibitedKeys[key]; found {
			return fmt.Errorf("contains prohibited private key %q", match[1])
		}
		if _, found := allowedKeys[key]; !found && !isSyntheticDynamicKey(string(match[1])) {
			return fmt.Errorf("contains non-allowlisted fixture key %q", match[1])
		}
	}
	return nil
}

func checkJSONL(path string, data []byte) error {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	line := 0
	for scanner.Scan() {
		line++
		var value any
		if err := strictDecode(scanner.Bytes(), &value); err != nil {
			if allowsMalformed(path) {
				continue
			}
			return fmt.Errorf("line %d is invalid JSON: %w", line, err)
		}
		if err := checkValue(value); err != nil {
			return fmt.Errorf("line %d: %w", line, err)
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("scan JSONL: %w", err)
	}
	return nil
}

func checkJSON(path string, data []byte) error {
	var value any
	if err := strictDecode(data, &value); err != nil {
		if allowsMalformed(path) {
			return nil
		}
		return fmt.Errorf("invalid JSON: %w", err)
	}
	return checkValue(value)
}

func strictDecode(data []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("trailing JSON value")
	}
	return nil
}

func checkValue(value any) error {
	return checkValueAt(value, "")
}

func checkValueAt(value any, parentKey string) error {
	switch value := value.(type) {
	case map[string]any:
		for key, child := range value {
			normalized := normalizeKey(key)
			if _, found := prohibitedKeys[normalized]; found {
				return fmt.Errorf("contains prohibited key %q", key)
			}
			_, allowed := allowedKeys[normalized]
			if normalizeKey(parentKey) == normalizeKey("models") {
				// ccusage represents Codex model identifiers as dynamic object keys.
				// Only explicitly synthetic keys are permitted in committed fixtures.
				allowed = isSyntheticDynamicKey(key)
			}
			if !allowed {
				return fmt.Errorf("contains non-allowlisted key %q", key)
			}
			if err := checkValueAt(child, key); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range value {
			if err := checkValueAt(child, parentKey); err != nil {
				return err
			}
		}
	}
	return nil
}

func isSyntheticDynamicKey(value string) bool {
	normalized := strings.ToLower(value)
	return strings.Contains(normalized, "synthetic") && !strings.ContainsAny(value, "/\\@")
}

func normalizeKey(value string) string {
	var normalized strings.Builder
	for _, character := range strings.ToLower(value) {
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' {
			normalized.WriteRune(character)
		}
	}
	return normalized.String()
}

func allowsMalformed(path string) bool {
	// Malformed compatibility cases are opted in by name, but raw privacy scans
	// have already run before syntax errors are tolerated.
	name := strings.ToLower(filepath.Base(path))
	return strings.Contains(name, "malformed") || strings.Contains(name, "partial")
}
