-- +goose Up
-- In-app notices: new strong matches (one per day, kept current), a tracked
-- posting that closed, and reminders of interviews and next steps. key
-- de-duplicates per user; they go with the account.
CREATE TABLE notifications (
  id         BIGSERIAL PRIMARY KEY,
  user_id    BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  kind       TEXT NOT NULL CHECK (kind IN ('strong_matches', 'posting_closed', 'reminder')),
  key        TEXT NOT NULL,
  job_id     BIGINT REFERENCES jobs(id) ON DELETE CASCADE,
  title      TEXT NOT NULL,
  body       TEXT NOT NULL DEFAULT '',
  at         TIMESTAMPTZ, -- when a reminded event happens or a step is due
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  read_at    TIMESTAMPTZ,
  UNIQUE (user_id, key)
);
CREATE INDEX notifications_user_created ON notifications (user_id, created_at DESC);

-- +goose Down
DROP TABLE notifications;
