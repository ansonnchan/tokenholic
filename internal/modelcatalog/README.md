# Model catalog and pricing

This package resolves model metadata independently from source adapters. An
adapter reports exactly what local evidence says; the catalog separately maps
that observed string to canonical identity, provider, and pricing key, and the
price registry turns token counts into an API-equivalent estimate.

Both the CLI and the API build the same bundled catalog. The server's result is
authoritative; the client's is an offline preview.

## Where prices come from

| File | Role | Edited by |
| --- | --- | --- |
| `data/litellm.lock.json` | Pins the upstream file: repository, path, full commit, SHA-256, retrieval date, and the providers and modes to keep | A reviewer, when moving the pin |
| `data/litellm_prices.json` | The vendored subset of that file | `make prices` only |
| `data/overrides.json` | Reviewed additions: prices the snapshot lacks, and labels deliberately left unpriced | A reviewer, with a citation |
| `data/LICENSE-litellm.txt` | MIT notice for the vendored data | Nobody |

The upstream file is LiteLLM's `model_prices_and_context_window.json`.
`cmd/pricegen` downloads the pinned commit, refuses content whose SHA-256
differs from the pin, and writes a deterministic snapshot. It keeps an entry
only when it is:

- a bare first-party model ID for a pinned provider and mode, not a prefixed
  route through another vendor;
- priced per token, with an input or output rate;
- not an `alias_of` entry. A moving alias such as a `-latest` name would
  reprice history whenever the provider repoints it.

For each kept model it stores rate cards and nothing else. A rate card is five
rates: uncached input, cache read, base cache write, 1-hour cache write, and
output. Every model has a standard card, and the generator adds the other
cards the upstream file publishes (see [Rate cards](#rate-cards)). Rates are
integer micro-US-dollars per million tokens, converted exactly from the
upstream decimal. A price that is not a whole number in that unit fails
generation instead of being rounded.

Every upstream field that starts with one of the four per-token price names
must match the shape the generator knows: a category, an optional cache
lifetime, an optional prompt-length threshold, and an optional service tier.
A field in a new shape, or with a tier nobody has reviewed, stops generation
rather than being dropped unseen.

### Moving the pin

1. Choose an upstream commit and set `commit`, `sha256`, and `retrieved` in
   `data/litellm.lock.json`. The digest is of the raw file at that commit.
2. Run `make prices`. It needs network access; `go run ./cmd/pricegen -input
   <file>` reads a local copy instead and applies the same checksum.
3. Review the diff of `data/litellm_prices.json`.
4. Update `bundledRevision` in `bundled_test.go` to the new value the failing
   test prints. The revision is derived from the content, so it cannot be
   forgotten. The next API start reprices every stored bucket to it in one
   transaction; see [the server guide](../../server/README.md).

### Overrides

`overrides.json` exists for models newer than the snapshot. Every price entry
must cite the provider's own pricing page over HTTPS and the date it was read;
`Build` rejects any other host. Copy the published numbers. Never estimate a
price from a similar model, and never add an entry without reading the page.
When no official price exists, leave the model out so it stays unpriced.

The `unpriced` list records identifiers that are understood and intentionally
have no model, such as a client routing label. Each carries a reason, a note,
and the page that was checked, so it reads as a decision rather than a gap.

The bundled overrides currently add no prices. The snapshot's rates for the
Claude models in the regression test were compared with
<https://platform.claude.com/docs/en/about-claude/pricing> on November 21, 2026
and matched.

## Rate cards

A provider can charge several prices for one model. Two facts about a request
select among them, and both travel with usage as
[pricing evidence](../normalize/README.md#pricing-evidence):

| Fact | Upstream source | Vendored as |
| --- | --- | --- |
| Service tier | A `_priority` or `_flex` suffix on a price field | `tiers.priority`, `tiers.flex` |
| Fast mode | `provider_specific_entry.fast`, a multiplier, on Anthropic models | `tiers.fast`: every standard rate times the multiplier |
| Prompt length | An `_above_<N>k_tokens` infix | `prompt_threshold` and `long_prompt`, per tier |

The fast-mode multiplier is the only derived card. Anthropic publishes fast
mode as a multiple of the standard rates with the prompt-caching multipliers
applied on top, across the whole context window
(<https://platform.claude.com/docs/en/about-claude/pricing>, read November 21,
2026). The products matched that page's fast-mode table. A product that is not
a whole rate, or a multiplier beside a prompt threshold, fails generation.

Deliberately not vendored:

- `_batches`, `_ultrafast`, and `_decisions` tiers. No supported coding agent
  runs through them, so their cards could never be selected.
- The 12-hour cache-write lifetime. No token category carries it.
- `provider_specific_entry.us`, the data-residency multiplier. Usage does not
  carry where a request was processed.

A model has at most one prompt threshold, and it must be a limit of the upload
ladder (100K, 200K, or 272K tokens). Otherwise no usage bucket could prove
which side a request fell on, so generation fails instead.

Each card has its own pricing key. The standard card keeps the model's bare
key, `provider/model`; the others append what selects them, such as
`openai/gpt-5.6-sol:priority:above-272k`. The key stored beside a usage bucket
therefore names the exact card that priced it.

### Selecting a card

`PricingRegistry.Select` applies these rules, in order:

| Evidence | Card |
| --- | --- |
| Proven service tier with a vendored card | That tier's card |
| Proven service tier without one | None: `tier_rate_unavailable` |
| No service tier | The standard card |
| Model has a threshold; the prompt band is within it | The tier's base card |
| Model has a threshold; the prompt band is above it | The tier's long-prompt card, or `tier_rate_unavailable` if none is published |
| Model has a threshold; no prompt band, or one that straddles it | None: `prompt_length_unproven` |

Pricing an unproven tier as standard is a product decision made on November
21, 2026, not evidence. Standard is what a provider applies when a request
names no tier. Codex records the tier only from a thread's second turn, so the
alternative left every first turn unpriced and put Codex-heavy users below the
coverage a cost rank requires. The estimate understates a first turn that
really ran at a higher tier. Prompt length has no such default: a model that
charges more above a threshold is priced only from proof of the side.

Changing a selection rule changes what stored usage prices to. Increment
`rulesVersion` in `bundled.go` in the same change so the revision moves.

## Resolution rules

Resolution tries an exact catalog ID first. Two explicit suffix rules follow,
and each requires an exact match for what remains:

| Rule | Example | Recorded match |
| --- | --- | --- |
| Strip a client context-window marker | `claude-opus-5-5[1m]` → `claude-opus-5-5` | `reviewed` |
| Strip a trailing calendar date, `-YYYYMMDD` or `-YYYY-MM-DD` | a dated snapshot ID → its undated model | `alias` |

A rule never guesses a model family, version, or size, and it only ever weakens
the recorded match kind. `[1m]` is safe to strip because it never selects a
price: a model whose price changes with prompt length is priced from each
request's proven prompt size, and every other model charges the same rates
across the whole context window.

Routing labels `auto`, `default`, `router`, and `smart` stay unknown, with or
without a suffix. Source-inferred attribution stays labelled `inferred` after a
canonical match.

Changing a rule changes what stored usage resolves to. Increment
`rulesVersion` in `bundled.go` in the same change so the revision moves.

## Why a model can be unpriced

Every derivation without a pricing key carries a reason. The first six
describe the model; the last two describe one bucket of a model that does have
prices:

| Reason | Meaning |
| --- | --- |
| `unknown_model` | The identifier is not in the catalog |
| `model_unavailable` | The source reported no model |
| `ambiguous_routing_alias` | A routing alias that names no single model |
| `routing_label` | A reviewed client label with no identifiable model |
| `no_published_price` | A known model with no reviewed price |
| `prompt_length_unproven` | The model charges more above a prompt length, and the bucket does not prove which side its requests fell on |
| `tier_rate_unavailable` | The bucket proves a service tier or prompt length with no vendored rate |

Usage collected before pricing evidence existed, and usage from a source that
cannot report a request's prompt size, has no prompt band. For a model with a
threshold it stays `prompt_length_unproven`; pricing it at the base rate would
be a guess.

Unpriced tokens still count toward token totals. They are reported as unpriced
so cost coverage stays visible.

## Estimates

`EstimateTokens` prices five categories:

| Tokens | Rate |
| --- | --- |
| Uncached input | Input |
| Cache read | Cache read |
| Cache write, 5-minute lifetime | Base cache write |
| Cache write, 1-hour lifetime | 1-hour cache write |
| Output, including reasoning | Output |

A cache write whose lifetime the source did not report is priced at the base
rate and marks the estimate a lower bound. An estimate is also a lower bound
when a billable category is unavailable or when known tokens have no rate; the
latter are counted in `UnpricedTokens`. Arithmetic is exact big-integer math
with one floor to whole micro-dollars per estimate.

Estimates use the rate card the bucket's evidence selects and global-routing
rates. Data-residency multipliers are not applied, because usage does not carry
where a request was processed.

Every value is labelled an `API-equivalent estimate`. It is not subscription
usage or actual spending.
