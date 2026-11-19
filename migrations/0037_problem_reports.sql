-- +goose Up
-- Problems people report from the extension ("Report a problem"): what
-- they wrote, the page, the extension's version and its diagnostics (what
-- it read on the page and what happened; self-identification answers are
-- left out by the extension). They go with the account when it is deleted.
CREATE TABLE problem_reports (
  id          BIGSERIAL PRIMARY KEY,
  user_id     BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  source      TEXT NOT NULL DEFAULT 'extension',
  note        TEXT NOT NULL DEFAULT '',
  page        TEXT NOT NULL DEFAULT '',
  version     TEXT NOT NULL DEFAULT '',
  details     TEXT NOT NULL DEFAULT '',
  resolved_at TIMESTAMPTZ
);
CREATE INDEX problem_reports_open_idx ON problem_reports (id DESC) WHERE resolved_at IS NULL;

-- +goose Down
DROP TABLE problem_reports;
