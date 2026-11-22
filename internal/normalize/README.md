# Normalized usage contract

The `normalize` package is the provider-neutral boundary between source adapters and every later stage. Adapters may understand Claude or Codex records, but collection, persistence, synchronization, and presentation should depend on normalized observations instead of provider JSON.

## Absence is not zero

Every token category is a pointer. A non-nil pointer to `0` means the source reported that category with a zero value. A nil pointer means the category was unavailable. Consumers must preserve that difference rather than filling unavailable categories with zeroes.

Reasoning output is subset metadata when a source exposes it. It is already included in output and is therefore excluded from `KnownTotal`.

Cache writes follow the same subset rule. `InputCacheWrite` is the total written at every cache lifetime and is the only cache-write value in `KnownTotal`. `InputCacheWrite5m` and `InputCacheWrite1h` split that total when the source reports a time-to-live. A nil lifetime means the time-to-live is unknown, not that nothing was written at it; `CacheWriteUnknownTTL` returns the part of the total no lifetime accounts for. Validation rejects lifetimes without a total or lifetimes that sum above it.

## Model identity

`ObservedModelID` is the identifier present in or attributable from source evidence. It is intentionally separate from the future canonical model identifier, provider, and pricing key.

Resolution values mean:

- `reported`: the source directly supplied the observed model identifier;
- `resolved`: tokenholic mapped an observed identifier to canonical metadata;
- `inferred`: the identifier is attributable only through surrounding source context and carries a warning;
- `unknown`: the available record cannot be attributed safely.

New observed model strings do not require an allowlist update. Unknown pricing must not discard their token counts.

## Pricing evidence

Providers charge different rates for the same model depending on how a request ran. Two facts decide it, and an observation carries each only when the source proves it:

- `ServiceTier`: the processing tier the request ran at (`standard`, `priority`, `flex`, `batch`, or `fast`). The empty value means the source did not say. It does not mean standard.
- `PromptTokens`: the size of the request's whole prompt, cached part included. It is set only on a record that is exactly one request, so a cumulative snapshot never has one.

A usage bucket cannot carry a size for many requests, so it carries a `PromptBand` instead: the request's place on a fixed ladder of the prompt lengths at which providers change price (100K, 200K, and 272K tokens). Every request in a band sits on the same side of each ladder limit. The ladder belongs to the upload contract, not to a catalog, so a client never needs to know which model has which threshold. A price threshold that is not on the ladder cannot be proven by any band, and the price generator refuses to vendor one.

Adapters report evidence; they never choose a price. What an unproven fact means is decided once, in `internal/modelcatalog`.

## Count and measurement semantics

Incremental observations represent usage contributed by one record. Cumulative snapshots represent counters up to a point in a session and must be reduced before aggregation. Measurement quality records whether a value was reported by the source, derived deterministically, or estimated.

These axes are independent. For example, a reported cumulative counter becomes a derived incremental observation after reduction.

## Day-level sources

Five sources have no native reader: GitHub Copilot CLI (`copilot`), Gemini CLI (`gemini`), Grok Build CLI (`grok`), OpenCode (`opencode`), and Qwen Code (`qwen`). Their usage is known only as one total per UTC day and model, so a record from them has a third count semantics, `day_total`: the whole of one day for one model as known when it was read. The day's total grows until the day ends, and a later reading replaces the record. It is never added to it and never reduced like a running counter.

A day total is filed at the start of its UTC day, on the `cli` surface. It carries no session, service tier, or prompt size, because it spans many requests. Where its usage fell against a local midnight is unknown, which is why the bucket built from it is marked with the granularity `day` and boards label the boundary as approximate. In a timezone behind UTC, usage from before the UTC date changes is therefore shown under the previous local date.

The tie between these sources and day totals holds in both directions and is checked in one place, `ValidateGranularity`, plus `Observation.Validate`: a day-level source can report nothing finer, and Claude or Codex can never be stored as a day total. That second half is what keeps the native sources out of the coarse path whatever produced a record.

## Capabilities and health

Capabilities describe how a source can provide each field, including explicit `unsupported` states. Source health describes whether a compatible evidence root was detected and readable. Neither contract converts missing coverage into a zero usage value.

`unavailable` is the health of a source whose records exist while this install cannot read them. It applies to the day-level sources, which are read by a separate program that can be absent or turned off, and a warning code names the reason. It is not `not_found`: the agent is in use, and its usage is unknown.

## Validation

`Observation.Validate` protects the boundary by checking source/surface combinations, version identifiers, model-resolution requirements, token availability, and warning safety. Validate observations before persistence or serialization so downstream packages do not need to reinterpret malformed provider data.
