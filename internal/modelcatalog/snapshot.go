package modelcatalog

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/ansonnchan/tokenholic/internal/normalize"
)

// SnapshotPin identifies the exact upstream price file that is vendored and
// the subset of it tokenholic keeps.
type SnapshotPin struct {
	Repository string   `json:"repository"`
	Path       string   `json:"path"`
	Commit     string   `json:"commit"`
	SHA256     string   `json:"sha256"`
	Retrieved  string   `json:"retrieved"`
	Providers  []string `json:"providers"`
	Modes      []string `json:"modes"`
	// PrefixedProviders lists the providers whose own models the upstream
	// file publishes as "<provider>/<model>". For these the prefix marks the
	// first-party price and is removed; for every other provider a prefix
	// marks another vendor's route and the entry is dropped.
	PrefixedProviders []string `json:"prefixed_providers,omitempty"`
	// ExcludedModels are models of a pinned provider left out on purpose,
	// each because its price cannot be applied from what a bucket proves.
	// They resolve as unknown models and stay unpriced.
	ExcludedModels []string `json:"excluded_models,omitempty"`
}

// Snapshot is the vendored, token-priced subset of the pinned upstream file.
// cmd/pricegen writes it; nothing else should edit it.
type Snapshot struct {
	Source SnapshotPin     `json:"source"`
	Models []SnapshotModel `json:"models"`
}

// SnapshotRates is one rate card. Rates are micro-US-dollars per million
// tokens; an absent rate means the provider publishes none for that category.
type SnapshotRates struct {
	Input        *uint64 `json:"input,omitempty"`
	CacheRead    *uint64 `json:"cache_read,omitempty"`
	CacheWrite   *uint64 `json:"cache_write,omitempty"`
	CacheWrite1h *uint64 `json:"cache_write_1h,omitempty"`
	Output       *uint64 `json:"output,omitempty"`
}

// SnapshotTier is a non-standard service tier's rate card, with the card
// charged above the model's prompt threshold when the provider publishes one.
type SnapshotTier struct {
	SnapshotRates
	LongPrompt *SnapshotRates `json:"long_prompt,omitempty"`
}

// SnapshotModel is one first-party model. Its own rates are the standard
// service tier at or below PromptThreshold. LongPrompt is the standard tier
// above that threshold, and Tiers holds every other service tier keyed by its
// normalize.ServiceTier name. A model with UnpricedReason resolves
// canonically but carries no rates.
type SnapshotModel struct {
	ID             string `json:"id"`
	Provider       string `json:"provider"`
	UnpricedReason string `json:"unpriced_reason,omitempty"`
	SnapshotRates
	// PromptThreshold is the prompt size in tokens above which the provider
	// charges the LongPrompt cards. Absent when price ignores prompt length.
	PromptThreshold *uint64                 `json:"prompt_threshold,omitempty"`
	LongPrompt      *SnapshotRates          `json:"long_prompt,omitempty"`
	Tiers           map[string]SnapshotTier `json:"tiers,omitempty"`
}

// priced reports whether the card carries any rate.
func (r SnapshotRates) priced() bool {
	return r.Input != nil || r.CacheRead != nil || r.CacheWrite != nil || r.CacheWrite1h != nil || r.Output != nil
}

// URL returns the immutable location of the pinned upstream file.
func (p SnapshotPin) URL() string {
	return "https://raw.githubusercontent.com/" + p.Repository + "/" + p.Commit + "/" + p.Path
}

// Validate checks that the pin names one immutable, checksummed file.
func (p SnapshotPin) Validate() error {
	if p.Repository == "" || p.Path == "" || len(p.Providers) == 0 || len(p.Modes) == 0 {
		return errors.New("price snapshot pin requires a repository, path, providers, and modes")
	}
	if len(p.Commit) != 40 || strings.Trim(p.Commit, "0123456789abcdef") != "" {
		return errors.New("price snapshot pin requires a full lowercase commit hash, not a branch or tag")
	}
	if len(p.SHA256) != 64 || strings.Trim(p.SHA256, "0123456789abcdef") != "" {
		return errors.New("price snapshot pin requires a lowercase SHA-256 digest")
	}
	if _, err := time.Parse(time.DateOnly, p.Retrieved); err != nil {
		return fmt.Errorf("price snapshot pin retrieval date: %w", err)
	}
	for _, provider := range p.PrefixedProviders {
		if !slices.Contains(p.Providers, provider) {
			return fmt.Errorf("price snapshot pin: prefixed provider %q is not a pinned provider", provider)
		}
	}
	for index, model := range p.ExcludedModels {
		if model == "" || slices.Contains(p.ExcludedModels[:index], model) {
			return errors.New("price snapshot pin: excluded models must be named once each")
		}
	}
	return nil
}

// DecodeSnapshot strictly parses a vendored snapshot and checks its ordering.
func DecodeSnapshot(data []byte) (Snapshot, error) {
	var snapshot Snapshot
	if err := decodeStrict(data, &snapshot); err != nil {
		return Snapshot{}, fmt.Errorf("decode price snapshot: %w", err)
	}
	if err := snapshot.Source.Validate(); err != nil {
		return Snapshot{}, err
	}
	if !sort.SliceIsSorted(snapshot.Models, func(i, j int) bool { return snapshot.Models[i].ID < snapshot.Models[j].ID }) {
		return Snapshot{}, errors.New("price snapshot models are not sorted by ID")
	}
	for index, model := range snapshot.Models {
		if model.ID == "" || model.Provider == "" {
			return Snapshot{}, fmt.Errorf("price snapshot model %d is missing its ID or provider", index)
		}
		if index > 0 && snapshot.Models[index-1].ID == model.ID {
			return Snapshot{}, fmt.Errorf("price snapshot model %q is duplicated", model.ID)
		}
		if model.priced() == (model.UnpricedReason != "") {
			return Snapshot{}, fmt.Errorf("price snapshot model %q must have either rates or an unpriced reason", model.ID)
		}
		if err := model.validateCards(); err != nil {
			return Snapshot{}, fmt.Errorf("price snapshot model %q: %w", model.ID, err)
		}
	}
	return snapshot, nil
}

// validateCards checks that every extra rate card is priced, names a known
// service tier, and that long-prompt cards exist only beside a threshold the
// upload ladder can prove.
func (m SnapshotModel) validateCards() error {
	long := m.LongPrompt != nil
	for name, tier := range m.Tiers {
		if name == string(normalize.ServiceTierStandard) || name == "" || !normalize.ServiceTier(name).Valid() {
			return fmt.Errorf("service tier %q cannot have its own rate card", name)
		}
		if !tier.priced() || tier.LongPrompt != nil && !tier.LongPrompt.priced() {
			return fmt.Errorf("service tier %q has a rate card without rates", name)
		}
		long = long || tier.LongPrompt != nil
	}
	if m.LongPrompt != nil && !m.LongPrompt.priced() {
		return errors.New("long-prompt rate card has no rates")
	}
	if m.UnpricedReason != "" && (len(m.Tiers) > 0 || long || m.PromptThreshold != nil) {
		return errors.New("an unpriced model cannot carry rate cards")
	}
	if long != (m.PromptThreshold != nil) {
		return errors.New("long-prompt rate cards and a prompt threshold must appear together")
	}
	if m.PromptThreshold != nil && !slices.Contains(normalize.PromptLadderLimits(), *m.PromptThreshold) {
		// No prompt band could prove either side of this threshold.
		return fmt.Errorf("prompt threshold %d is not on the upload ladder", *m.PromptThreshold)
	}
	return nil
}

func decodeStrict(data []byte, target any) error {
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if decoder.More() {
		return errors.New("trailing JSON value")
	}
	return nil
}
