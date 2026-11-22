-- Day-level sources: coding agents with no native reader, whose usage is
-- known only as one total per UTC day and model (count semantics day_total).
--
-- SQLite cannot widen a CHECK constraint, so the three tables that list the
-- sources are rebuilt with the wider lists and their rows copied unchanged.
-- Nothing is deleted and no Claude or Codex row is altered. Source health
-- also gains the status "unavailable", for a day-level source whose records
-- exist while the program that reads them is absent or turned off.
--
-- Foreign keys are enforced while this runs. adapter_health refers to
-- source_checkpoints with ON DELETE CASCADE, so the copy of the child is made
-- against the copy of the parent, the old child is dropped before the old
-- parent, and the parent is renamed first: renaming a table also repoints
-- the references to it.

CREATE TABLE observations_new (
    identity TEXT PRIMARY KEY,
    source TEXT NOT NULL CHECK (source IN ('claude', 'codex', 'copilot', 'gemini', 'grok', 'opencode', 'qwen')),
    surface TEXT NOT NULL CHECK (surface IN ('cli', 'desktop_code', 'desktop_agent', 'desktop_cowork', 'desktop', 'shared_local')),
    source_version TEXT NOT NULL CHECK (source_version <> ''),
    session_fingerprint TEXT,
    occurred_at TEXT NOT NULL,
    observed_model_id TEXT NOT NULL,
    canonical_model_id TEXT,
    model_provider TEXT,
    pricing_key TEXT,
    model_resolution TEXT NOT NULL CHECK (model_resolution IN ('reported', 'resolved', 'inferred', 'unknown')),
    input_uncached_tokens TEXT CHECK (input_uncached_tokens IS NULL OR (length(input_uncached_tokens) > 0 AND input_uncached_tokens NOT GLOB '*[^0-9]*')),
    input_cache_read_tokens TEXT CHECK (input_cache_read_tokens IS NULL OR (length(input_cache_read_tokens) > 0 AND input_cache_read_tokens NOT GLOB '*[^0-9]*')),
    input_cache_write_tokens TEXT CHECK (input_cache_write_tokens IS NULL OR (length(input_cache_write_tokens) > 0 AND input_cache_write_tokens NOT GLOB '*[^0-9]*')),
    output_tokens TEXT CHECK (output_tokens IS NULL OR (length(output_tokens) > 0 AND output_tokens NOT GLOB '*[^0-9]*')),
    reasoning_output_tokens TEXT CHECK (reasoning_output_tokens IS NULL OR (length(reasoning_output_tokens) > 0 AND reasoning_output_tokens NOT GLOB '*[^0-9]*')),
    count_semantics TEXT NOT NULL CHECK (count_semantics IN ('incremental', 'cumulative_snapshot', 'day_total')),
    measurement TEXT NOT NULL CHECK (measurement IN ('reported', 'derived', 'estimated')),
    adapter_version TEXT NOT NULL CHECK (adapter_version <> ''),
    normalization_version TEXT NOT NULL CHECK (normalization_version <> ''),
    warnings_json TEXT NOT NULL CHECK (json_valid(warnings_json)),
    input_cache_write_5m_tokens TEXT
        CHECK (input_cache_write_5m_tokens IS NULL OR (length(input_cache_write_5m_tokens) > 0 AND input_cache_write_5m_tokens NOT GLOB '*[^0-9]*')),
    input_cache_write_1h_tokens TEXT
        CHECK (input_cache_write_1h_tokens IS NULL OR (length(input_cache_write_1h_tokens) > 0 AND input_cache_write_1h_tokens NOT GLOB '*[^0-9]*')),
    service_tier TEXT
        CHECK (service_tier IS NULL OR (length(service_tier) BETWEEN 1 AND 32 AND service_tier NOT GLOB '*[^a-z_]*')),
    prompt_tokens TEXT
        CHECK (prompt_tokens IS NULL OR (length(prompt_tokens) > 0 AND prompt_tokens NOT GLOB '*[^0-9]*')),
    CHECK (
        (source = 'claude' AND surface IN ('cli', 'desktop_code', 'desktop_agent', 'desktop_cowork')) OR
        (source = 'codex' AND surface IN ('cli', 'desktop', 'shared_local')) OR
        (source IN ('copilot', 'gemini', 'grok', 'opencode', 'qwen') AND surface = 'cli')
    ),
    -- A day total belongs to a day-level source and to nothing else, so a
    -- Claude or Codex row can never be stored as one.
    CHECK ((count_semantics = 'day_total') = (source IN ('copilot', 'gemini', 'grok', 'opencode', 'qwen')))
) STRICT;

INSERT INTO observations_new (
    identity, source, surface, source_version, session_fingerprint, occurred_at,
    observed_model_id, canonical_model_id, model_provider, pricing_key, model_resolution,
    input_uncached_tokens, input_cache_read_tokens, input_cache_write_tokens, output_tokens,
    reasoning_output_tokens, count_semantics, measurement, adapter_version,
    normalization_version, warnings_json, input_cache_write_5m_tokens,
    input_cache_write_1h_tokens, service_tier, prompt_tokens
)
SELECT
    identity, source, surface, source_version, session_fingerprint, occurred_at,
    observed_model_id, canonical_model_id, model_provider, pricing_key, model_resolution,
    input_uncached_tokens, input_cache_read_tokens, input_cache_write_tokens, output_tokens,
    reasoning_output_tokens, count_semantics, measurement, adapter_version,
    normalization_version, warnings_json, input_cache_write_5m_tokens,
    input_cache_write_1h_tokens, service_tier, prompt_tokens
FROM observations;

DROP TABLE observations;
ALTER TABLE observations_new RENAME TO observations;
CREATE INDEX observations_occurred_at ON observations(occurred_at, identity);
CREATE INDEX observations_model ON observations(canonical_model_id, observed_model_id);

CREATE TABLE source_checkpoints_new (
    source_identity TEXT PRIMARY KEY,
    source TEXT NOT NULL CHECK (source IN ('claude', 'codex', 'copilot', 'gemini', 'grok', 'opencode', 'qwen')),
    surface TEXT NOT NULL CHECK (surface IN ('cli', 'desktop_code', 'desktop_agent', 'desktop_cowork', 'desktop', 'shared_local')),
    cursor_value TEXT NOT NULL CHECK (length(cursor_value) > 0 AND cursor_value NOT GLOB '*[^0-9]*'),
    last_record_fingerprint TEXT,
    collected_at TEXT NOT NULL,
    CHECK (
        (source = 'claude' AND surface IN ('cli', 'desktop_code', 'desktop_agent', 'desktop_cowork')) OR
        (source = 'codex' AND surface IN ('cli', 'desktop', 'shared_local')) OR
        (source IN ('copilot', 'gemini', 'grok', 'opencode', 'qwen') AND surface = 'cli')
    )
) STRICT;

INSERT INTO source_checkpoints_new (source_identity, source, surface, cursor_value, last_record_fingerprint, collected_at)
SELECT source_identity, source, surface, cursor_value, last_record_fingerprint, collected_at
FROM source_checkpoints;

CREATE TABLE adapter_health_new (
    source_identity TEXT PRIMARY KEY REFERENCES source_checkpoints_new(source_identity) ON DELETE CASCADE,
    source TEXT NOT NULL CHECK (source IN ('claude', 'codex', 'copilot', 'gemini', 'grok', 'opencode', 'qwen')),
    surface TEXT NOT NULL CHECK (surface IN ('cli', 'desktop_code', 'desktop_agent', 'desktop_cowork', 'desktop', 'shared_local')),
    -- unavailable: the records exist but this install cannot read them.
    status TEXT NOT NULL CHECK (status IN ('detected', 'not_found', 'permission_denied', 'unsupported', 'degraded', 'error', 'unavailable')),
    record_count TEXT CHECK (record_count IS NULL OR (length(record_count) > 0 AND record_count NOT GLOB '*[^0-9]*')),
    format TEXT NOT NULL,
    capabilities_json TEXT NOT NULL CHECK (json_valid(capabilities_json)),
    warnings_json TEXT NOT NULL CHECK (json_valid(warnings_json)),
    adapter_version TEXT NOT NULL CHECK (adapter_version <> ''),
    checked_at TEXT NOT NULL,
    last_success_at TEXT,
    CHECK (
        (source = 'claude' AND surface IN ('cli', 'desktop_code', 'desktop_agent', 'desktop_cowork')) OR
        (source = 'codex' AND surface IN ('cli', 'desktop', 'shared_local')) OR
        (source IN ('copilot', 'gemini', 'grok', 'opencode', 'qwen') AND surface = 'cli')
    )
) STRICT;

INSERT INTO adapter_health_new (
    source_identity, source, surface, status, record_count, format, capabilities_json,
    warnings_json, adapter_version, checked_at, last_success_at
)
SELECT
    source_identity, source, surface, status, record_count, format, capabilities_json,
    warnings_json, adapter_version, checked_at, last_success_at
FROM adapter_health;

DROP TABLE adapter_health;
DROP TABLE source_checkpoints;
ALTER TABLE source_checkpoints_new RENAME TO source_checkpoints;
ALTER TABLE adapter_health_new RENAME TO adapter_health;
