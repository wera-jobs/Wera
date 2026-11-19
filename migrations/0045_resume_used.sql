-- +goose Up
-- Which resume (and whether a letter) went with an application: as the
-- extension attached it on each autofill run, and as the user says on the
-- application (which wins).
ALTER TABLE autofill_runs
  ADD COLUMN resume_kind TEXT CHECK (resume_kind IN ('tailored', 'base', 'upload')),
  ADD COLUMN resume_name TEXT,
  ADD COLUMN letter      BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE applications
  ADD COLUMN resume_kind TEXT CHECK (resume_kind IN ('tailored', 'base', 'upload')),
  ADD COLUMN letter_sent BOOLEAN;

-- +goose Down
ALTER TABLE autofill_runs DROP COLUMN resume_kind, DROP COLUMN resume_name, DROP COLUMN letter;
ALTER TABLE applications DROP COLUMN resume_kind, DROP COLUMN letter_sent;
