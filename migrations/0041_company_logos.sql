-- +goose Up
-- Each company's logo, found on its job board and served by Wera so a
-- browser never asks a third party. found = false records a look that came
-- up empty (rechecked like a logo, monthly).
CREATE TABLE company_logos (
  company_id   BIGINT PRIMARY KEY REFERENCES companies(id) ON DELETE CASCADE,
  found        BOOLEAN NOT NULL,
  content_type TEXT,
  data         BYTEA,
  source_url   TEXT,
  checked_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE company_logos;
