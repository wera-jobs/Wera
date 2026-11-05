-- +goose NO TRANSACTION
-- +goose Up
-- user_jobs_user_stage_idx (0030) repeats user_jobs_stage_idx (0004), the
-- same (user_id, stage) index: it served no query and slowed every write
-- of a user's matches, hundreds of thousands of rows for a new user.
DROP INDEX CONCURRENTLY IF EXISTS user_jobs_user_stage_idx;

-- +goose Down
CREATE INDEX CONCURRENTLY IF NOT EXISTS user_jobs_user_stage_idx ON user_jobs (user_id, stage);
