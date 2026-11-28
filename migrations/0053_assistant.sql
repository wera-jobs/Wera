-- +goose Up
-- Ask Wera: one conversation per thread, optionally about a job. The user
-- can delete a thread or all of them; deleting the user deletes both.
CREATE TABLE assistant_threads (
  id          BIGSERIAL PRIMARY KEY,
  user_id     BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  job_id      BIGINT REFERENCES jobs(id) ON DELETE SET NULL,
  title       TEXT NOT NULL,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX assistant_threads_user_idx ON assistant_threads (user_id, updated_at DESC);

CREATE TABLE assistant_messages (
  id          BIGSERIAL PRIMARY KEY,
  thread_id   BIGINT NOT NULL REFERENCES assistant_threads(id) ON DELETE CASCADE,
  user_id     BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  role        TEXT NOT NULL CHECK (role IN ('user', 'assistant')),
  content     TEXT NOT NULL,
  -- The data an answer names ("resume", "job", ...), for the citations.
  sources     TEXT[] NOT NULL DEFAULT '{}',
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX assistant_messages_thread_idx ON assistant_messages (thread_id, id);
-- The daily question limit counts a user's own messages.
CREATE INDEX assistant_messages_user_idx ON assistant_messages (user_id, created_at) WHERE role = 'user';

-- +goose Down
DROP TABLE assistant_messages;
DROP TABLE assistant_threads;
