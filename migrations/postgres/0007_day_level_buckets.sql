-- Day-level buckets: usage from a coding agent the client has no reader of
-- its own for, known only as one total per UTC day and model.
--
-- interval_seconds already records how much time a bucket covers and was
-- fixed at one minute. It now also admits one day, 86400. A day bucket is the
-- absolute total of one agent, day, and model, filed at the start of its UTC
-- day; a later revision replaces it as the day grows, exactly as a minute
-- bucket is replaced. Where its usage fell against a group's local midnight
-- is not known, and boards say so from this column.
--
-- Additive: every existing row is a minute bucket and stays valid. The API
-- validates which sources report days; the schema checks only the shape.
ALTER TABLE usage_buckets DROP CONSTRAINT usage_buckets_interval_seconds_check;
ALTER TABLE usage_buckets
    ADD CONSTRAINT usage_buckets_interval_seconds_check CHECK (interval_seconds IN (60, 86400)),
    ADD CONSTRAINT usage_buckets_day_start_check CHECK (
        interval_seconds <> 86400 OR
        date_trunc('day', bucket_start AT TIME ZONE 'UTC') = bucket_start AT TIME ZONE 'UTC'
    );

COMMENT ON COLUMN usage_buckets.interval_seconds IS 'Time the bucket covers: 60 for a minute of usage, 86400 for a day-level total filed at the start of its UTC day.';
