-- +goose Up
-- Several named base resumes per user (one for each kind of role they
-- want), one of them the default. Tailored copies remember which base they
-- were made from.
ALTER TABLE resumes
  ADD COLUMN is_default BOOLEAN NOT NULL DEFAULT false,
  ADD COLUMN base_id    BIGINT REFERENCES resumes(id) ON DELETE SET NULL,
  ADD CONSTRAINT resumes_default_is_base CHECK (NOT is_default OR job_id IS NULL);
UPDATE resumes SET is_default = true WHERE job_id IS NULL;
DROP INDEX resumes_base_idx;
CREATE UNIQUE INDEX resumes_default_idx ON resumes (user_id) WHERE is_default;
CREATE INDEX resumes_base_id_idx ON resumes (base_id) WHERE base_id IS NOT NULL;

-- +goose Down
DELETE FROM resumes WHERE job_id IS NULL AND NOT is_default;
DROP INDEX resumes_base_id_idx;
DROP INDEX resumes_default_idx;
CREATE UNIQUE INDEX resumes_base_idx ON resumes (user_id) WHERE job_id IS NULL;
ALTER TABLE resumes DROP CONSTRAINT resumes_default_is_base, DROP COLUMN base_id, DROP COLUMN is_default;
