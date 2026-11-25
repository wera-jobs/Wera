-- +goose Up
-- People a user is in touch with about one application: recruiters,
-- hiring managers, referrals. When they last spoke and when to follow up.
CREATE TABLE user_job_contacts (
  id                BIGSERIAL PRIMARY KEY,
  user_id           BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  job_id            BIGINT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
  name              TEXT NOT NULL,
  role              TEXT NOT NULL DEFAULT '',
  email             TEXT NOT NULL DEFAULT '',
  linkedin_url      TEXT NOT NULL DEFAULT '',
  notes             TEXT NOT NULL DEFAULT '',
  last_contacted_at TIMESTAMPTZ,
  follow_up_at      TIMESTAMPTZ,
  created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX user_job_contacts_user_job ON user_job_contacts (user_id, job_id);
CREATE INDEX user_job_contacts_job ON user_job_contacts (job_id);

-- +goose Down
DROP TABLE user_job_contacts;
