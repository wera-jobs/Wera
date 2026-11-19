-- +goose Up
-- The pay a posting states, read from its text by internal/pay (the same
-- rules as the extension): one figure or a range, its currency and
-- whether it is hourly or yearly. pay_checked says the text was read, so
-- jobs saved before this column existed are read once in the background.
ALTER TABLE jobs
  ADD COLUMN pay_min      NUMERIC(12,2),
  ADD COLUMN pay_max      NUMERIC(12,2),
  ADD COLUMN pay_currency TEXT,
  ADD COLUMN pay_period   TEXT CHECK (pay_period IN ('hour', 'year')),
  ADD COLUMN pay_checked  BOOLEAN NOT NULL DEFAULT false;
CREATE INDEX jobs_pay_unchecked_idx ON jobs (id) WHERE NOT pay_checked AND closed_at IS NULL;

-- +goose Down
DROP INDEX jobs_pay_unchecked_idx;
ALTER TABLE jobs DROP COLUMN pay_min, DROP COLUMN pay_max, DROP COLUMN pay_currency, DROP COLUMN pay_period, DROP COLUMN pay_checked;
