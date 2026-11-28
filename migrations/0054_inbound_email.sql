-- +goose Up
-- A user's own forwarding address for recruiter replies (opt-in, revocable:
-- deleting the row turns it off; a new token retires the old address).
CREATE TABLE inbound_addresses (
  user_id        BIGINT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  token          TEXT NOT NULL UNIQUE,
  created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
  last_email_at  TIMESTAMPTZ,
  -- Gmail's forwarding confirmation, shown to the user to finish setup.
  confirm_code   TEXT,
  confirm_url    TEXT,
  confirm_at     TIMESTAMPTZ
);

-- What a forwarded reply suggested for the tracker. Only the match and the
-- suggestion are kept: never the message body.
CREATE TABLE email_suggestions (
  id           BIGSERIAL PRIMARY KEY,
  user_id      BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  job_id       BIGINT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
  kind         TEXT NOT NULL CHECK (kind IN ('rejection','offer','interview','assessment','received')),
  status       TEXT NOT NULL CHECK (status IN ('applied','interviewing','offer','rejected')),
  from_domain  TEXT NOT NULL,
  subject      TEXT NOT NULL,
  received_at  TIMESTAMPTZ NOT NULL,
  state        TEXT NOT NULL DEFAULT 'pending' CHECK (state IN ('pending','accepted','dismissed')),
  decided_at   TIMESTAMPTZ,
  created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- One pending suggestion per job and status: a second email updates it.
CREATE UNIQUE INDEX email_suggestions_pending_idx ON email_suggestions (user_id, job_id, status) WHERE state = 'pending';

-- +goose Down
DROP TABLE email_suggestions;
DROP TABLE inbound_addresses;
