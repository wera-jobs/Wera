-- +goose Up
-- Quick grades: the AI's ratings of a user's unscored matches, many jobs
-- per call and without a written reason (scoring.QuickGrader). They order
-- the queue and show as estimates until a full score replaces them.
-- score_key is the scoring key of the profile text they were made for.
CREATE TABLE quick_grades (
  user_id    BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  job_id     BIGINT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
  score_key  TEXT NOT NULL,
  score      INT NOT NULL CHECK (score BETWEEN 0 AND 100),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (user_id, job_id)
);
CREATE INDEX quick_grades_job_idx ON quick_grades (job_id);

-- +goose Down
DROP TABLE quick_grades;
