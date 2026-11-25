-- +goose Up
-- Practice threads: a user's answers to one interview question for one job
-- and the follow-ups after it, each with its feedback, so the practice is
-- still there when the job is opened again.
CREATE TABLE interview_practice (
  user_id       BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  job_id        BIGINT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
  question_hash TEXT NOT NULL,
  question      TEXT NOT NULL,
  turns         JSONB NOT NULL DEFAULT '[]',
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (user_id, job_id, question_hash)
);
CREATE INDEX interview_practice_job ON interview_practice (job_id);

-- +goose Down
DROP TABLE interview_practice;
