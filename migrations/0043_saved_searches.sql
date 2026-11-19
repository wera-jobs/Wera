-- +goose Up
-- Named Jobs filter sets: query is the Jobs page's query string (filter
-- keys only), so a saved search reopens with the same results.
CREATE TABLE saved_searches (
  id         BIGSERIAL PRIMARY KEY,
  user_id    BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name       TEXT NOT NULL,
  query      TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX saved_searches_user ON saved_searches (user_id, created_at);

-- +goose Down
DROP TABLE saved_searches;
