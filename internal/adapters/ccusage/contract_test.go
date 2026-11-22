package ccusage

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type contractCounts struct {
	InputTokens           *uint64 `json:"inputTokens"`
	OutputTokens          *uint64 `json:"outputTokens"`
	CacheCreationTokens   *uint64 `json:"cacheCreationTokens"`
	CacheReadTokens       *uint64 `json:"cacheReadTokens"`
	ReasoningOutputTokens *uint64 `json:"reasoningOutputTokens,omitempty"`
	TotalTokens           *uint64 `json:"totalTokens,omitempty"`
}

type claudeContract struct {
	Daily  []claudeDay   `json:"daily"`
	Totals *claudeTotals `json:"totals"`
}

type claudeDay struct {
	contractCounts
	Date            string        `json:"date"`
	ModelsUsed      []string      `json:"modelsUsed"`
	ModelBreakdowns []claudeModel `json:"modelBreakdowns"`
}

type claudeModel struct {
	contractCounts
	ModelName             string  `json:"modelName"`
	CacheCreation1hTokens *uint64 `json:"cacheCreation1hTokens,omitempty"`
	MissingPricing        *bool   `json:"missingPricing,omitempty"`
}

type claudeTotals struct {
	contractCounts
	UnpricedModels []string `json:"unpricedModels,omitempty"`
}

type codexContract struct {
	Daily  []codexDay   `json:"daily"`
	Totals *codexTotals `json:"totals"`
}

type codexDay struct {
	contractCounts
	Date   string                `json:"date"`
	Models map[string]codexModel `json:"models"`
}

type codexModel struct {
	contractCounts
	IsFallback     *bool `json:"isFallback,omitempty"`
	MissingPricing *bool `json:"missingPricing,omitempty"`
}

type codexTotals struct {
	contractCounts
	UnpricedModels []string `json:"unpricedModels,omitempty"`
}

type totals struct {
	input, output, cacheWrite, cacheRead, reasoning, total uint64
}

func TestPinnedFixturesMatchContract(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		validate func([]byte) error
	}{
		{name: "claude-daily.json", validate: validateClaudeContract},
		{name: "codex-daily.json", validate: validateCodexContract},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			data, err := os.ReadFile(filepath.Join("..", "..", "..", "fixtures", "ccusage", test.name))
			if err != nil {
				t.Fatal(err)
			}
			if err := test.validate(data); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestContractRejectsInvalidReports(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		data     string
		validate func([]byte) error
	}{
		{name: "missing totals", data: `{"daily":[]}`, validate: validateClaudeContract},
		{name: "unknown field", data: `{"daily":[],"extra":1,"totals":{"inputTokens":0,"outputTokens":0,"cacheCreationTokens":0,"cacheReadTokens":0,"totalTokens":0}}`, validate: validateClaudeContract},
		{name: "duplicate field", data: `{"daily":[],"daily":[],"totals":{"inputTokens":0,"outputTokens":0,"cacheCreationTokens":0,"cacheReadTokens":0,"totalTokens":0}}`, validate: validateClaudeContract},
		{name: "inconsistent total", data: `{"daily":[],"totals":{"inputTokens":1,"outputTokens":0,"cacheCreationTokens":0,"cacheReadTokens":0,"totalTokens":0}}`, validate: validateClaudeContract},
		{name: "missing reasoning", data: `{"daily":[],"totals":{"inputTokens":0,"outputTokens":0,"cacheCreationTokens":0,"cacheReadTokens":0,"totalTokens":0}}`, validate: validateCodexContract},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if err := test.validate([]byte(test.data)); err == nil {
				t.Fatal("validation error = nil")
			}
		})
	}
}

func TestLockMatchesCompiledContract(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "ccusage.lock.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateLock(data); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name     string
		old, new string
	}{
		{name: "duplicate key", old: `"tool": "ccusage"`, new: `"tool": "ccusage", "tool": "ccusage"`},
		{name: "another version", old: `"version": "20.0.51"`, new: `"version": "20.0.52"`},
		{name: "a floating version", old: `"version": "20.0.51"`, new: `"version": "^20.0.51"`},
		{name: "an added argument", old: `"--offline", `, new: `"--offline", "--since", "20260101", `},
		{name: "a dropped offline flag", old: `"--offline", `, new: ``},
		{name: "a native agent", old: `"agents": ["copilot"`, new: `"agents": ["claude", "copilot"`},
		{name: "another executable digest", old: `"sha256": "81a9af33`, new: `"sha256": "00a9af33`},
		{name: "a missing npm integrity", old: `"integrity": "sha512-Z8dP`, new: `"integrity": "sha1-Z8dP`},
		{name: "another package", old: `"package": "@ccusage/ccusage-linux-x64"`, new: `"package": "ccusage"`},
		{name: "an unknown field", old: `"tool": "ccusage"`, new: `"tool": "ccusage", "note": "x"`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			changed := bytes.Replace(data, []byte(test.old), []byte(test.new), 1)
			if bytes.Equal(changed, data) {
				t.Fatal("the lock no longer contains the text this case edits")
			}
			if err := ValidateLock(changed); err == nil {
				t.Fatal("a lock that differs from the compiled contract was accepted")
			}
		})
	}
}

func TestEveryPlatformHasAPinnedDigest(t *testing.T) {
	t.Parallel()
	for _, platform := range [][2]string{
		{"darwin", "arm64"}, {"darwin", "amd64"}, {"linux", "arm64"}, {"linux", "amd64"},
		{"windows", "arm64"}, {"windows", "amd64"},
	} {
		if digest, found := PinnedDigest(platform[0], platform[1]); !found || len(digest) != 64 {
			t.Errorf("PinnedDigest(%s, %s) = %q, %t", platform[0], platform[1], digest, found)
		}
	}
	if _, found := PinnedDigest("freebsd", "amd64"); found {
		t.Error("a platform ccusage does not ship has a digest")
	}
}

func validateClaudeContract(data []byte) error {
	var report claudeContract
	if err := strictContractDecode(data, &report); err != nil {
		return err
	}
	if report.Daily == nil || report.Totals == nil {
		return errors.New("daily and totals are required")
	}
	seenDates := make(map[string]struct{})
	var reportTotal totals
	for _, day := range report.Daily {
		if err := validateContractDate(day.Date, seenDates); err != nil {
			return err
		}
		dayTotal, err := day.contractCounts.values(false, true)
		if err != nil {
			return fmt.Errorf("daily period %q: %w", day.Date, err)
		}
		if len(day.ModelBreakdowns) == 0 || day.ModelsUsed == nil {
			return fmt.Errorf("daily period %q requires model breakdowns and modelsUsed", day.Date)
		}
		models := make(map[string]struct{})
		var modelTotal totals
		for _, model := range day.ModelBreakdowns {
			if model.ModelName == "" {
				return fmt.Errorf("daily period %q has an empty model name", day.Date)
			}
			if _, duplicate := models[model.ModelName]; duplicate {
				return fmt.Errorf("daily period %q repeats model %q", day.Date, model.ModelName)
			}
			models[model.ModelName] = struct{}{}
			counts, err := model.contractCounts.values(false, false)
			if err != nil {
				return err
			}
			if model.CacheCreation1hTokens != nil && *model.CacheCreation1hTokens > counts.cacheWrite {
				return errors.New("invalid 1h cache split")
			}
			if err := modelTotal.add(counts); err != nil {
				return err
			}
		}
		if modelTotal != dayTotal || !sameModels(models, day.ModelsUsed) {
			return errors.New("daily model breakdown does not match declared totals")
		}
		if err := reportTotal.add(dayTotal); err != nil {
			return err
		}
	}
	declared, err := report.Totals.contractCounts.values(false, true)
	if err != nil {
		return err
	}
	if reportTotal != declared {
		return errors.New("report totals do not match daily periods")
	}
	return nil
}

func validateCodexContract(data []byte) error {
	var report codexContract
	if err := strictContractDecode(data, &report); err != nil {
		return err
	}
	if report.Daily == nil || report.Totals == nil {
		return errors.New("daily and totals are required")
	}
	seenDates := make(map[string]struct{})
	var reportTotal totals
	for _, day := range report.Daily {
		if err := validateContractDate(day.Date, seenDates); err != nil {
			return err
		}
		dayTotal, err := day.contractCounts.values(true, true)
		if err != nil {
			return err
		}
		if len(day.Models) == 0 {
			return errors.New("daily period has no models")
		}
		var modelTotal totals
		for name, model := range day.Models {
			if name == "" {
				return errors.New("daily period has an empty model name")
			}
			counts, err := model.contractCounts.values(true, true)
			if err != nil {
				return err
			}
			if err := modelTotal.add(counts); err != nil {
				return err
			}
		}
		if modelTotal != dayTotal {
			return errors.New("daily model breakdown does not match declared totals")
		}
		if err := reportTotal.add(dayTotal); err != nil {
			return err
		}
	}
	declared, err := report.Totals.contractCounts.values(true, true)
	if err != nil {
		return err
	}
	if reportTotal != declared {
		return errors.New("report totals do not match daily periods")
	}
	return nil
}

func (c contractCounts) values(requireReasoning, requireTotal bool) (totals, error) {
	if c.InputTokens == nil || c.OutputTokens == nil || c.CacheCreationTokens == nil || c.CacheReadTokens == nil ||
		requireReasoning && c.ReasoningOutputTokens == nil || requireTotal && c.TotalTokens == nil {
		return totals{}, errors.New("required token count is missing or null")
	}
	value := totals{input: *c.InputTokens, output: *c.OutputTokens, cacheWrite: *c.CacheCreationTokens, cacheRead: *c.CacheReadTokens}
	if c.ReasoningOutputTokens != nil {
		value.reasoning = *c.ReasoningOutputTokens
		if value.reasoning > value.output {
			return totals{}, errors.New("reasoning output exceeds output")
		}
	}
	calculated, err := sumContractValues(value.input, value.output, value.cacheWrite, value.cacheRead)
	if err != nil {
		return totals{}, err
	}
	value.total = calculated
	if c.TotalTokens != nil && *c.TotalTokens != calculated {
		return totals{}, errors.New("totalTokens does not match token categories")
	}
	return value, nil
}

func (t *totals) add(value totals) error {
	fields := []struct {
		target *uint64
		value  uint64
	}{
		{&t.input, value.input}, {&t.output, value.output}, {&t.cacheWrite, value.cacheWrite},
		{&t.cacheRead, value.cacheRead}, {&t.reasoning, value.reasoning}, {&t.total, value.total},
	}
	for _, field := range fields {
		if math.MaxUint64-*field.target < field.value {
			return errors.New("token count overflows uint64")
		}
		*field.target += field.value
	}
	return nil
}

func sumContractValues(values ...uint64) (uint64, error) {
	var total uint64
	for _, value := range values {
		if math.MaxUint64-total < value {
			return 0, errors.New("token count overflows uint64")
		}
		total += value
	}
	return total, nil
}

func validateContractDate(value string, seen map[string]struct{}) error {
	if _, duplicate := seen[value]; duplicate {
		return fmt.Errorf("duplicate daily period %q", value)
	}
	seen[value] = struct{}{}
	if _, err := time.Parse("2006-01-02", value); err != nil {
		return fmt.Errorf("invalid daily period %q", value)
	}
	return nil
}

func sameModels(models map[string]struct{}, used []string) bool {
	if len(models) != len(used) {
		return false
	}
	seen := make(map[string]struct{}, len(used))
	for _, model := range used {
		if _, found := models[model]; !found {
			return false
		}
		if _, duplicate := seen[model]; duplicate {
			return false
		}
		seen[model] = struct{}{}
	}
	return true
}

func strictContractDecode(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := rejectContractDuplicates(decoder); err != nil {
		return err
	}
	decoder = json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return errors.New("trailing JSON value")
	}
	return nil
}

func rejectContractDuplicates(decoder *json.Decoder) error {
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
			key, err := decoder.Token()
			if err != nil {
				return err
			}
			text, ok := key.(string)
			if !ok {
				return errors.New("object key is not a string")
			}
			if _, duplicate := keys[text]; duplicate {
				return fmt.Errorf("duplicate JSON key %q", text)
			}
			keys[text] = struct{}{}
			if err := rejectContractDuplicates(decoder); err != nil {
				return err
			}
		}
	case '[':
		for decoder.More() {
			if err := rejectContractDuplicates(decoder); err != nil {
				return err
			}
		}
	}
	_, err = decoder.Token()
	return err
}
