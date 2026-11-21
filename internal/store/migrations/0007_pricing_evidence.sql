-- Pricing evidence: facts about one request that decide which of a model's
-- prices applies. NULL means the source did not prove the fact; it is never
-- the standard tier or a short prompt.
--
-- Rows stored before this migration stay NULL until their source file is read
-- again by an adapter revision that reports the evidence. Rows whose file is
-- gone keep NULL, so their usage bucket keeps its existing key.
ALTER TABLE observations ADD COLUMN service_tier TEXT
    CHECK (service_tier IS NULL OR (length(service_tier) BETWEEN 1 AND 32 AND service_tier NOT GLOB '*[^a-z_]*'));
ALTER TABLE observations ADD COLUMN prompt_tokens TEXT
    CHECK (prompt_tokens IS NULL OR (length(prompt_tokens) > 0 AND prompt_tokens NOT GLOB '*[^0-9]*'));
