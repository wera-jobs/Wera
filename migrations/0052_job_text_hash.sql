-- +goose NO TRANSACTION
-- +goose Up
-- One role posted once per office city has the same title and text in
-- every posting; only the location differs, so content_hash differs too.
-- text_hash leaves the location out: postings of a company that share it
-- are the same role, which lets one facts reading serve them all and a
-- feed show the role once. A trigger keeps it current, so every writer of
-- jobs gets it without knowing the formula.
ALTER TABLE jobs ADD COLUMN IF NOT EXISTS text_hash TEXT;

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION jobs_set_text_hash() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  NEW.text_hash := md5(lower(btrim(NEW.title)) || E'\n' || coalesce(NEW.description, ''));
  RETURN NEW;
END
$$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS jobs_text_hash ON jobs;
CREATE TRIGGER jobs_text_hash BEFORE INSERT OR UPDATE OF title, description ON jobs
  FOR EACH ROW EXECUTE FUNCTION jobs_set_text_hash();

-- Open postings only: closed ones are not shown or read for facts again.
UPDATE jobs SET text_hash = md5(lower(btrim(title)) || E'\n' || coalesce(description, ''))
WHERE closed_at IS NULL AND text_hash IS NULL;

CREATE INDEX CONCURRENTLY IF NOT EXISTS jobs_text_hash_idx ON jobs (company_id, text_hash)
  WHERE text_hash IS NOT NULL;

-- +goose Down
DROP INDEX IF EXISTS jobs_text_hash_idx;
DROP TRIGGER IF EXISTS jobs_text_hash ON jobs;
DROP FUNCTION IF EXISTS jobs_set_text_hash();
ALTER TABLE jobs DROP COLUMN IF EXISTS text_hash;
