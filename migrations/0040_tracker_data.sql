-- +goose Up
-- What a tracker keeps per application beyond its status and notes: the
-- next step and when, one contact, and whether it is archived (off the
-- board, history kept). Interviews and other dated events get their own
-- table.
ALTER TABLE applications
  ADD COLUMN next_step    TEXT,
  ADD COLUMN next_step_at TIMESTAMPTZ,
  ADD COLUMN contact      JSONB,
  ADD COLUMN archived_at  TIMESTAMPTZ;

CREATE TABLE application_events (
  id         BIGSERIAL PRIMARY KEY,
  user_id    BIGINT NOT NULL,
  job_id     BIGINT NOT NULL,
  at         TIMESTAMPTZ NOT NULL,
  kind       TEXT NOT NULL CHECK (kind IN ('phone_screen', 'interview', 'onsite', 'assessment', 'offer_call', 'other')),
  with_whom  TEXT NOT NULL DEFAULT '',
  notes      TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  FOREIGN KEY (user_id, job_id) REFERENCES applications (user_id, job_id) ON DELETE CASCADE
);
CREATE INDEX application_events_user_at ON application_events (user_id, at);
CREATE INDEX application_events_job ON application_events (user_id, job_id);
CREATE INDEX applications_next_step_at ON applications (next_step_at) WHERE next_step_at IS NOT NULL;

-- +goose Down
DROP TABLE application_events;
ALTER TABLE applications
  DROP COLUMN next_step,
  DROP COLUMN next_step_at,
  DROP COLUMN contact,
  DROP COLUMN archived_at;
