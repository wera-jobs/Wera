-- +goose NO TRANSACTION
-- +goose Up
-- Each run reads every board's postings by company (stored hashes, known
-- ids, still listed, closed), and with no index each read scanned all
-- 857,000 postings: 67 ms a read, thousands a run. With it, under 1 ms.
CREATE INDEX CONCURRENTLY IF NOT EXISTS jobs_company_idx ON jobs (company_id);
-- A user's matches by stage: a user has a few thousand live matches among
-- hundreds of thousands of excluded ones, and the dashboard's lists and
-- counts read only the live ones.
CREATE INDEX CONCURRENTLY IF NOT EXISTS user_jobs_user_stage_idx ON user_jobs (user_id, stage);

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS user_jobs_user_stage_idx;
DROP INDEX CONCURRENTLY IF EXISTS jobs_company_idx;
