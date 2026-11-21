-- Pricing evidence: the provider service tier a bucket's requests ran at and
-- where their prompt sizes fall on the upload ladder. A provider prices both,
-- so the server needs them to choose among a model's rate cards.
--
-- Additive. NULL means the client did not prove the fact; it is never the
-- standard tier or a short prompt, and existing rows keep NULL. Both columns
-- are client evidence: they are covered by payload_hash and read back when
-- history is repriced. The API validates the names; the schema checks only
-- their shape, so a new tier or ladder step needs no migration.
ALTER TABLE usage_buckets
    ADD COLUMN service_tier TEXT CHECK (service_tier IS NULL OR service_tier ~ '^[a-z][a-z0-9_]{0,31}$'),
    ADD COLUMN prompt_band TEXT CHECK (prompt_band IS NULL OR prompt_band ~ '^[a-z][a-z0-9_]{0,31}$');

COMMENT ON COLUMN usage_buckets.service_tier IS 'Client evidence: provider service tier of every request in the bucket. NULL is unproven, not standard.';
COMMENT ON COLUMN usage_buckets.prompt_band IS 'Client evidence: prompt-size band shared by every request in the bucket. NULL is unproven, not short.';
