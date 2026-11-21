package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/ansonnchan/tokenholic/internal/modelcatalog"
)

// upstream is a synthetic file in the upstream shape. It exercises every
// keep and drop rule without copying real price data into the test.
const upstream = `{
  "sample_spec": {"litellm_provider": "one of the providers", "mode": "chat", "input_cost_per_token": 0.0},
  "model-flat": {"litellm_provider": "anthropic", "mode": "chat", "input_cost_per_token": 0.000004,
    "output_cost_per_token": 0.00002, "cache_read_input_token_cost": 2E-7,
    "cache_creation_input_token_cost": 0.000005, "cache_creation_input_token_cost_above_1hr": 0.000008,
    "input_cost_per_token_batches": 0.000002, "max_input_tokens": 1000000, "supports_vision": true,
    "provider_specific_entry": {"us": 1.1}},
  "model-fraction": {"litellm_provider": "openai", "mode": "responses", "input_cost_per_token": 3.125E-7, "output_cost_per_token": 1.5625e-06},
  "model-quick": {"litellm_provider": "anthropic", "mode": "chat", "input_cost_per_token": 0.000004,
    "output_cost_per_token": 0.00002, "cache_read_input_token_cost": 2E-7,
    "cache_creation_input_token_cost": 0.000005, "cache_creation_input_token_cost_above_1hr": 0.000008,
    "provider_specific_entry": {"us": 1.1, "fast": 2.0}},
  "model-tiered": {"litellm_provider": "openai", "mode": "chat", "input_cost_per_token": 0.000004,
    "output_cost_per_token": 0.00002, "cache_read_input_token_cost": 4E-7,
    "input_cost_per_token_above_272k_tokens": 0.000008, "output_cost_per_token_above_272k_tokens": 0.00003,
    "input_cost_per_token_priority": 0.000008, "output_cost_per_token_priority": 0.00004,
    "input_cost_per_token_above_272k_tokens_priority": 0.000016,
    "input_cost_per_token_flex": 0.000002, "output_cost_per_token_flex": 0.00001,
    "cache_creation_input_token_cost_above_12hr": 0.00001, "cache_creation_input_token_cost_above_12hr_above_272k_tokens_priority": 0.00004,
    "input_cost_per_token_ultrafast": 0.00004, "output_cost_per_token_above_272k_tokens_batches": 0.000015,
    "input_cost_per_token_decisions": 0.000001, "input_cost_per_audio_token": 0.00004,
    "provider_specific_entry": {"fast": 3}},
  "model-latest": {"alias_of": "model-flat"},
  "other/model-flat": {"litellm_provider": "anthropic", "mode": "chat", "input_cost_per_token": 0.000004, "output_cost_per_token": 0.00002},
  "model-embedding": {"litellm_provider": "openai", "mode": "embedding", "input_cost_per_token": 1E-7},
  "model-image": {"litellm_provider": "openai", "mode": "chat", "output_cost_per_image": 0.04},
  "model-elsewhere": {"litellm_provider": "bedrock", "mode": "chat", "input_cost_per_token": 0.000004, "output_cost_per_token": 0.00002}
}`

func TestBuildKeepsOnlyFirstPartyTokenPrices(t *testing.T) {
	t.Parallel()
	snapshot, err := build(pinFor(upstream), []byte(upstream))
	if err != nil {
		t.Fatal(err)
	}
	first, err := encode(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	// The whole vendored form is compared, so a card that should not exist
	// fails the test as surely as a wrong rate. Skipped upstream tiers
	// (batches, ultrafast, decisions), the 12-hour cache lifetime, the
	// data-residency multiplier, and a fast multiplier on a provider it was
	// not reviewed for leave no trace.
	const wantModels = `[
    {
      "id": "model-flat",
      "provider": "anthropic",
      "input": 4000000,
      "cache_read": 200000,
      "cache_write": 5000000,
      "cache_write_1h": 8000000,
      "output": 20000000
    },
    {
      "id": "model-fraction",
      "provider": "openai",
      "input": 312500,
      "output": 1562500
    },
    {
      "id": "model-quick",
      "provider": "anthropic",
      "input": 4000000,
      "cache_read": 200000,
      "cache_write": 5000000,
      "cache_write_1h": 8000000,
      "output": 20000000,
      "tiers": {
        "fast": {
          "input": 8000000,
          "cache_read": 400000,
          "cache_write": 10000000,
          "cache_write_1h": 16000000,
          "output": 40000000
        }
      }
    },
    {
      "id": "model-tiered",
      "provider": "openai",
      "input": 4000000,
      "cache_read": 400000,
      "output": 20000000,
      "prompt_threshold": 272000,
      "long_prompt": {
        "input": 8000000,
        "output": 30000000
      },
      "tiers": {
        "flex": {
          "input": 2000000,
          "output": 10000000
        },
        "priority": {
          "input": 8000000,
          "output": 40000000,
          "long_prompt": {
            "input": 16000000
          }
        }
      }
    }
  ]`
	if !strings.Contains(string(first), `"models": `+wantModels) {
		t.Fatalf("generated snapshot models differ:\n%s\nwant models:\n%s", first, wantModels)
	}

	again, err := build(pinFor(upstream), []byte(upstream))
	if err != nil {
		t.Fatal(err)
	}
	second, err := encode(again)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) || !bytes.HasSuffix(first, []byte("}\n")) {
		t.Fatal("generated snapshot is not deterministic")
	}
	if _, _, err := modelcatalog.Build(first, []byte(`{"prices":[],"unpriced":[]}`)); err != nil {
		t.Fatalf("generated snapshot does not build a catalog: %v", err)
	}
}

func TestBuildRefusesUnverifiedOrUnrepresentableInput(t *testing.T) {
	t.Parallel()
	const subMicro = `{"model-a": {"litellm_provider": "anthropic", "mode": "chat", "input_cost_per_token": 1E-13, "output_cost_per_token": 0.00002}}`
	const negative = `{"model-a": {"litellm_provider": "anthropic", "mode": "chat", "input_cost_per_token": -0.000004, "output_cost_per_token": 0.00002}}`
	const textual = `{"model-a": {"litellm_provider": "anthropic", "mode": "chat", "input_cost_per_token": "free", "output_cost_per_token": 0.00002}}`
	const unrelated = `{"model-a": {"litellm_provider": "bedrock", "mode": "chat", "input_cost_per_token": 0.000004}}`
	model := func(fields string) string {
		return `{"model-a": {"litellm_provider": "anthropic", "mode": "chat", "input_cost_per_token": 0.000004, "output_cost_per_token": 0.00002, ` + fields + `}}`
	}
	newTier := model(`"input_cost_per_token_turbo": 0.00001`)
	newShape := model(`"input_cost_per_token_per_second": 0.00001`)
	twoThresholds := model(`"input_cost_per_token_above_200k_tokens": 0.000008, "output_cost_per_token_above_272k_tokens": 0.00003`)
	offLadder := model(`"input_cost_per_token_above_128k_tokens": 0.000008`)
	lifetimeOnInput := model(`"input_cost_per_token_above_1hr": 0.000008`)
	longWithoutBase := model(`"input_cost_per_token_above_272k_tokens_priority": 0.000016, "input_cost_per_token_above_272k_tokens": 0.000008`)
	fractionalFast := model(`"cache_read_input_token_cost": 1E-12, "provider_specific_entry": {"fast": 2.5}`)
	negativeFast := model(`"provider_specific_entry": {"fast": -2}`)
	fastWithThreshold := model(`"input_cost_per_token_above_272k_tokens": 0.000008, "provider_specific_entry": {"fast": 2}`)
	tampered := pinFor(upstream)
	tests := []struct {
		name    string
		pin     modelcatalog.SnapshotPin
		data    string
		wantErr string
	}{
		{name: "checksum mismatch", pin: tampered, data: upstream + " ", wantErr: "SHA-256"},
		{name: "price finer than the unit", pin: pinFor(subMicro), data: subMicro, wantErr: "whole number"},
		{name: "negative price", pin: pinFor(negative), data: negative, wantErr: "non-negative"},
		{name: "non-numeric price", pin: pinFor(textual), data: textual, wantErr: "not a number"},
		{name: "nothing matched the pin", pin: pinFor(unrelated), data: unrelated, wantErr: "no upstream model"},
		{name: "unreviewed service tier", pin: pinFor(newTier), data: newTier, wantErr: "unreviewed service tier"},
		{name: "unrecognized price field", pin: pinFor(newShape), data: newShape, wantErr: "unrecognized price field"},
		{name: "two prompt thresholds", pin: pinFor(twoThresholds), data: twoThresholds, wantErr: "second prompt threshold"},
		{name: "threshold no band can prove", pin: pinFor(offLadder), data: offLadder, wantErr: "not on the upload ladder"},
		{name: "cache lifetime on an input price", pin: pinFor(lifetimeOnInput), data: lifetimeOnInput, wantErr: "cache lifetime"},
		{name: "long-prompt tier card without its base card", pin: pinFor(longWithoutBase), data: longWithoutBase, wantErr: "without rates"},
		{name: "fast multiplier that is not a whole rate", pin: pinFor(fractionalFast), data: fractionalFast, wantErr: "not a whole rate"},
		{name: "negative fast multiplier", pin: pinFor(negativeFast), data: negativeFast, wantErr: "positive multiplier"},
		{name: "fast multiplier beside a prompt threshold", pin: pinFor(fastWithThreshold), data: fastWithThreshold, wantErr: "not reviewed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Some shapes are only refused once the snapshot is validated.
			snapshot, err := build(tt.pin, []byte(tt.data))
			if err == nil {
				_, err = encode(snapshot)
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("build() and encode() error = %v, want substring %q", err, tt.wantErr)
			}
		})
	}
}

func pinFor(data string) modelcatalog.SnapshotPin {
	digest := sha256.Sum256([]byte(data))
	return modelcatalog.SnapshotPin{
		Repository: "example/prices", Path: "prices.json", Commit: strings.Repeat("a", 40),
		SHA256: hex.EncodeToString(digest[:]), Retrieved: "2026-11-21",
		Providers: []string{"anthropic", "openai"}, Modes: []string{"chat", "responses"},
	}
}
