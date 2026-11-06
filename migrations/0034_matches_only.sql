-- +goose NO TRANSACTION
-- +goose Up
-- user_jobs keeps a user's matches (and jobs they track or the AI ruled
-- out), not a row for every posting their rules reject: those were 98-99%
-- of its rows, about 900,000 per user. A rejected posting is remembered by
-- time instead: jobs.changed_at moves when a posting is added, changes or
-- reopens, and profiles.filtered_through is when the user's last filter
-- pass started, so a pass only reads postings changed since.
ALTER TABLE jobs ADD COLUMN IF NOT EXISTS changed_at TIMESTAMPTZ NOT NULL DEFAULT '-infinity';
ALTER TABLE jobs ALTER COLUMN changed_at SET DEFAULT now();
ALTER TABLE profiles ADD COLUMN IF NOT EXISTS filtered_through TIMESTAMPTZ;
-- Users filtered before have a row for every posting up to now; the rest
-- (NULL) get a full pass.
UPDATE profiles SET filtered_through = now() WHERE filtered_preferences IS NOT NULL;
DELETE FROM user_jobs uj
WHERE uj.stage = 'excluded' AND uj.analysis_id IS NULL
  AND NOT EXISTS (SELECT 1 FROM applications a WHERE a.user_id = uj.user_id AND a.job_id = uj.job_id);
VACUUM FULL ANALYZE user_jobs;
CREATE INDEX CONCURRENTLY IF NOT EXISTS jobs_changed_idx ON jobs (changed_at) WHERE closed_at IS NULL;
-- Deleting an analysis sets user_jobs.analysis_id to NULL: without this
-- index every deleted analysis scans user_jobs.
CREATE INDEX CONCURRENTLY IF NOT EXISTS user_jobs_analysis_idx ON user_jobs (analysis_id) WHERE analysis_id IS NOT NULL;

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS user_jobs_analysis_idx;
DROP INDEX CONCURRENTLY IF EXISTS jobs_changed_idx;
ALTER TABLE profiles DROP COLUMN IF EXISTS filtered_through;
ALTER TABLE jobs DROP COLUMN IF EXISTS changed_at;
