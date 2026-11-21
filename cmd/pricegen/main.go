// Command pricegen vendors the pinned LiteLLM price file into the model
// catalog. It downloads the exact commit named by the lock file, refuses any
// content whose SHA-256 differs, and writes only first-party token prices:
// the standard rates plus the rate cards the upstream file publishes for
// other service tiers and for prompts above a length threshold.
//
// Run it through `make prices` after changing the lock file. The output is
// deterministic, so an unchanged pin regenerates a byte-identical file.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"os"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ansonnchan/tokenholic/internal/modelcatalog"
	"github.com/ansonnchan/tokenholic/internal/normalize"
)

const (
	maxUpstreamBytes = 64 << 20
	downloadTimeout  = 2 * time.Minute
)

// priceField matches an upstream per-token price field:
//
//	<category>[_above_1hr|_above_12hr][_above_<N>k_tokens][_<tier>]
//
// The optional parts are a cache-write lifetime, the prompt length above which
// the price applies, and a service tier.
var priceField = regexp.MustCompile(`^(` + strings.Join(priceCategories, "|") + `)(_above_1hr|_above_12hr)?(?:_above_([0-9]+)k_tokens)?(?:_([a-z]+))?$`)

// priceCategories are the upstream names of the four per-token prices. Every
// field that starts with one must match priceField, so a new upstream shape
// stops generation instead of being dropped unseen.
var priceCategories = []string{
	"input_cost_per_token", "output_cost_per_token",
	"cache_read_input_token_cost", "cache_creation_input_token_cost",
}

// vendoredTiers maps an upstream tier suffix to the service tier whose rate
// card it fills. The empty suffix is the standard tier.
var vendoredTiers = map[string]normalize.ServiceTier{
	"":         normalize.ServiceTierStandard,
	"priority": normalize.ServiceTierPriority,
	"flex":     normalize.ServiceTierFlex,
}

// skippedTiers are upstream tiers deliberately not vendored: no supported
// coding agent runs through them, so their cards could never be selected.
// Usage that does prove one of them stays unpriced with a reason.
var skippedTiers = map[string]bool{"batches": true, "ultrafast": true, "decisions": true}

// fastModeProviders lists the providers whose provider_specific_entry.fast is
// reviewed as a multiplier on every standard rate. For Anthropic that is the
// published fast-mode price: a multiple of the standard rates, with the
// prompt-caching multipliers applied on top, across the whole context window
// (https://platform.claude.com/docs/en/about-claude/pricing, read 2026-11-21).
var fastModeProviders = map[string]bool{"anthropic": true}

// microUSDPerMillion converts USD per token to micro-USD per million tokens.
var microUSDPerMillion = new(big.Rat).SetInt(big.NewInt(1_000_000_000_000))

func main() {
	lockPath := flag.String("lock", "internal/modelcatalog/data/litellm.lock.json", "pinned upstream description")
	outputPath := flag.String("output", "internal/modelcatalog/data/litellm_prices.json", "vendored snapshot to write")
	inputPath := flag.String("input", "", "read the upstream file from disk instead of downloading it")
	flag.Parse()
	if err := run(context.Background(), *lockPath, *outputPath, *inputPath); err != nil {
		fmt.Fprintln(os.Stderr, "pricegen:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, lockPath, outputPath, inputPath string) error {
	lockData, err := os.ReadFile(lockPath)
	if err != nil {
		return fmt.Errorf("read price snapshot pin: %w", err)
	}
	var pin modelcatalog.SnapshotPin
	decoder := json.NewDecoder(bytes.NewReader(lockData))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&pin); err != nil {
		return fmt.Errorf("decode price snapshot pin: %w", err)
	}
	if err := pin.Validate(); err != nil {
		return err
	}
	var upstream []byte
	if inputPath != "" {
		upstream, err = os.ReadFile(inputPath)
	} else {
		upstream, err = download(ctx, pin.URL())
	}
	if err != nil {
		return err
	}
	snapshot, err := build(pin, upstream)
	if err != nil {
		return err
	}
	encoded, err := encode(snapshot)
	if err != nil {
		return err
	}
	if err := os.WriteFile(outputPath, encoded, 0o644); err != nil {
		return fmt.Errorf("write vendored price snapshot: %w", err)
	}
	fmt.Printf("vendored %d models from %s@%s\n", len(snapshot.Models), pin.Repository, pin.Commit[:12])
	return nil
}

func download(ctx context.Context, url string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, downloadTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("create upstream price request: %w", err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("download upstream prices: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download upstream prices: status %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxUpstreamBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read upstream prices: %w", err)
	}
	if len(data) > maxUpstreamBytes {
		return nil, errors.New("upstream price file exceeds the size limit")
	}
	return data, nil
}

// build verifies the upstream bytes against the pin and reduces them to the
// vendored subset. Nothing is parsed before the checksum matches.
func build(pin modelcatalog.SnapshotPin, upstream []byte) (modelcatalog.Snapshot, error) {
	digest := sha256.Sum256(upstream)
	if actual := hex.EncodeToString(digest[:]); actual != pin.SHA256 {
		return modelcatalog.Snapshot{}, fmt.Errorf("upstream price file SHA-256 is %s, pin expects %s", actual, pin.SHA256)
	}
	var entries map[string]json.RawMessage
	if err := json.Unmarshal(upstream, &entries); err != nil {
		return modelcatalog.Snapshot{}, fmt.Errorf("decode upstream prices: %w", err)
	}
	identifiers := make([]string, 0, len(entries))
	for identifier := range entries {
		identifiers = append(identifiers, identifier)
	}
	sort.Strings(identifiers)
	snapshot := modelcatalog.Snapshot{Source: pin}
	for _, identifier := range identifiers {
		model, keep, err := convert(pin, identifier, entries[identifier])
		if err != nil {
			return modelcatalog.Snapshot{}, fmt.Errorf("model %q: %w", identifier, err)
		}
		if keep {
			snapshot.Models = append(snapshot.Models, model)
		}
	}
	if len(snapshot.Models) == 0 {
		return modelcatalog.Snapshot{}, errors.New("no upstream model matched the pinned providers and modes")
	}
	return snapshot, nil
}

// convert keeps one upstream entry when it is a first-party, token-priced
// model. Prefixed routes and moving aliases are dropped: a route is another
// vendor's price, and an alias would reprice history when it moves.
func convert(pin modelcatalog.SnapshotPin, identifier string, raw json.RawMessage) (modelcatalog.SnapshotModel, bool, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || strings.ContainsAny(identifier, "/ ") {
		return modelcatalog.SnapshotModel{}, false, nil
	}
	if _, alias := fields["alias_of"]; alias {
		return modelcatalog.SnapshotModel{}, false, nil
	}
	provider, mode := text(fields["litellm_provider"]), text(fields["mode"])
	if !slices.Contains(pin.Providers, provider) || !slices.Contains(pin.Modes, mode) {
		return modelcatalog.SnapshotModel{}, false, nil
	}
	_, hasInput := fields["input_cost_per_token"]
	_, hasOutput := fields["output_cost_per_token"]
	if !hasInput && !hasOutput {
		return modelcatalog.SnapshotModel{}, false, nil
	}
	model := modelcatalog.SnapshotModel{ID: identifier, Provider: provider}
	// Fields are visited in name order so an error names the same field on
	// every run.
	names := make([]string, 0, len(fields))
	for field := range fields {
		names = append(names, field)
	}
	sort.Strings(names)
	type cardKey struct {
		tier normalize.ServiceTier
		long bool
	}
	cards := make(map[cardKey]*modelcatalog.SnapshotRates)
	for _, field := range names {
		if !slices.ContainsFunc(priceCategories, func(category string) bool { return strings.HasPrefix(field, category) }) {
			continue
		}
		parts := priceField.FindStringSubmatch(field)
		if parts == nil {
			return modelcatalog.SnapshotModel{}, false, fmt.Errorf("%s: unrecognized price field", field)
		}
		category, lifetime, thresholdText, suffix := parts[1], parts[2], parts[3], parts[4]
		if skippedTiers[suffix] {
			continue
		}
		tier, vendored := vendoredTiers[suffix]
		if !vendored {
			return modelcatalog.SnapshotModel{}, false, fmt.Errorf("%s: unreviewed service tier %q", field, suffix)
		}
		var target func(*modelcatalog.SnapshotRates) **uint64
		switch {
		case lifetime == "_above_12hr" && category == "cache_creation_input_token_cost":
			// No token category carries a 12-hour cache lifetime, so this
			// rate could never be applied.
			continue
		case lifetime == "_above_1hr" && category == "cache_creation_input_token_cost":
			target = func(rates *modelcatalog.SnapshotRates) **uint64 { return &rates.CacheWrite1h }
		case lifetime != "":
			return modelcatalog.SnapshotModel{}, false, fmt.Errorf("%s: a cache lifetime on a price that is not a cache write", field)
		case category == "input_cost_per_token":
			target = func(rates *modelcatalog.SnapshotRates) **uint64 { return &rates.Input }
		case category == "output_cost_per_token":
			target = func(rates *modelcatalog.SnapshotRates) **uint64 { return &rates.Output }
		case category == "cache_read_input_token_cost":
			target = func(rates *modelcatalog.SnapshotRates) **uint64 { return &rates.CacheRead }
		default:
			target = func(rates *modelcatalog.SnapshotRates) **uint64 { return &rates.CacheWrite }
		}
		long := thresholdText != ""
		if long {
			thousands, err := strconv.ParseUint(thresholdText, 10, 32)
			if err != nil || thousands == 0 {
				return modelcatalog.SnapshotModel{}, false, fmt.Errorf("%s: invalid prompt threshold", field)
			}
			threshold := thousands * 1000
			if model.PromptThreshold != nil && *model.PromptThreshold != threshold {
				return modelcatalog.SnapshotModel{}, false, fmt.Errorf("%s: a second prompt threshold; one model, one threshold is supported", field)
			}
			model.PromptThreshold = &threshold
		}
		rate, err := rateFromCostPerToken(fields[field])
		if err != nil {
			return modelcatalog.SnapshotModel{}, false, fmt.Errorf("%s: %w", field, err)
		}
		key := cardKey{tier: tier, long: long}
		if cards[key] == nil {
			cards[key] = &modelcatalog.SnapshotRates{}
		}
		*target(cards[key]) = &rate
	}
	if standard := cards[cardKey{tier: normalize.ServiceTierStandard}]; standard != nil {
		model.SnapshotRates = *standard
	}
	model.LongPrompt = cards[cardKey{tier: normalize.ServiceTierStandard, long: true}]
	for key, rates := range cards {
		if key.tier == normalize.ServiceTierStandard {
			continue
		}
		if model.Tiers == nil {
			model.Tiers = make(map[string]modelcatalog.SnapshotTier)
		}
		tier := model.Tiers[string(key.tier)]
		if key.long {
			tier.LongPrompt = rates
		} else {
			tier.SnapshotRates = *rates
		}
		model.Tiers[string(key.tier)] = tier
	}
	if fastModeProviders[provider] {
		if err := addFastMode(&model, fields["provider_specific_entry"]); err != nil {
			return modelcatalog.SnapshotModel{}, false, err
		}
	}
	return model, true, nil
}

// addFastMode vendors the fast-mode card from the multiplier the upstream
// entry publishes. Every standard rate is multiplied exactly; a product that
// is not a whole rate fails generation. A model that also has a prompt
// threshold is refused, because the multiplier does not say which side it
// applies to.
func addFastMode(model *modelcatalog.SnapshotModel, raw json.RawMessage) error {
	var entry map[string]json.Number
	if len(raw) == 0 {
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&entry); err != nil {
		return fmt.Errorf("provider_specific_entry: %w", err)
	}
	text, found := entry["fast"]
	if !found {
		return nil
	}
	multiplier, ok := new(big.Rat).SetString(text.String())
	if !ok || multiplier.Sign() <= 0 {
		return fmt.Errorf("provider_specific_entry.fast: %q is not a positive multiplier", text)
	}
	if model.PromptThreshold != nil {
		return errors.New("provider_specific_entry.fast: a fast-mode multiplier beside a prompt threshold is not reviewed")
	}
	fast := modelcatalog.SnapshotTier{}
	for _, pair := range []struct{ from, to **uint64 }{
		{&model.Input, &fast.Input}, {&model.CacheRead, &fast.CacheRead}, {&model.CacheWrite, &fast.CacheWrite},
		{&model.CacheWrite1h, &fast.CacheWrite1h}, {&model.Output, &fast.Output},
	} {
		if *pair.from == nil {
			continue
		}
		product := new(big.Rat).Mul(new(big.Rat).SetUint64(**pair.from), multiplier)
		if !product.IsInt() || !product.Num().IsUint64() {
			return fmt.Errorf("provider_specific_entry.fast: %s times a standard rate is not a whole rate", text)
		}
		rate := product.Num().Uint64()
		*pair.to = &rate
	}
	if model.Tiers == nil {
		model.Tiers = make(map[string]modelcatalog.SnapshotTier)
	}
	model.Tiers[string(normalize.ServiceTierFast)] = fast
	return nil
}

// rateFromCostPerToken converts the upstream decimal exactly. A price that is
// not a whole number of micro-dollars per million tokens is an error rather
// than a rounded value.
func rateFromCostPerToken(raw json.RawMessage) (uint64, error) {
	var number json.Number
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&number); err != nil {
		return 0, fmt.Errorf("price is not a number: %w", err)
	}
	value, ok := new(big.Rat).SetString(number.String())
	if !ok || value.Sign() < 0 {
		return 0, fmt.Errorf("price %q is not a non-negative decimal", number)
	}
	value.Mul(value, microUSDPerMillion)
	if !value.IsInt() || !value.Num().IsUint64() {
		return 0, fmt.Errorf("price %q is not a whole number of micro-USD per million tokens", number)
	}
	return value.Num().Uint64(), nil
}

func text(raw json.RawMessage) string {
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return ""
	}
	return value
}

func encode(snapshot modelcatalog.Snapshot) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(snapshot); err != nil {
		return nil, fmt.Errorf("encode vendored price snapshot: %w", err)
	}
	if _, err := modelcatalog.DecodeSnapshot(buffer.Bytes()); err != nil {
		return nil, fmt.Errorf("generated snapshot is invalid: %w", err)
	}
	return buffer.Bytes(), nil
}
