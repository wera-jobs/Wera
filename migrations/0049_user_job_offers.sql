-- +goose Up
-- An offer the user got for one job, to compare offers side by side.
-- Amounts are yearly, in currency; bonus_pct is a percent of base.
CREATE TABLE user_job_offers (
  user_id       BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  job_id        BIGINT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
  base_salary   NUMERIC(12,2),
  bonus_pct     NUMERIC(6,2),
  annual_equity NUMERIC(14,2),
  sign_on       NUMERIC(12,2),
  currency      TEXT NOT NULL DEFAULT 'USD',
  work_mode     TEXT NOT NULL DEFAULT '' CHECK (work_mode IN ('', 'remote', 'hybrid', 'onsite')),
  location      TEXT NOT NULL DEFAULT '',
  deadline_at   TIMESTAMPTZ,
  notes         TEXT NOT NULL DEFAULT '',
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (user_id, job_id)
);
CREATE INDEX user_job_offers_job ON user_job_offers (job_id);

-- +goose Down
DROP TABLE user_job_offers;
