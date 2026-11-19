-- +goose Up
-- Why the user passed on a job (Not interested): optional, one of a few
-- reasons. The ranker learns from it, and repeats suggest a preference.
ALTER TABLE applications ADD COLUMN dismiss_reason TEXT
  CHECK (dismiss_reason IN ('too_senior', 'too_junior', 'location', 'field', 'company', 'pay', 'other'));

-- +goose Down
ALTER TABLE applications DROP COLUMN dismiss_reason;
