-- +goose Up
-- Interview prep for one job: likely questions with the user's talking
-- points, a brief and questions to ask, written once and kept so reopening
-- the job costs nothing.
CREATE TABLE interview_preps (
  user_id    BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  job_id     BIGINT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
  prep       JSONB NOT NULL,
  model      TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (user_id, job_id)
);
CREATE INDEX interview_preps_job ON interview_preps (job_id);

-- +goose Down
DROP TABLE interview_preps;
