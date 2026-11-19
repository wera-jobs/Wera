-- +goose Up
-- Companies a user follows: their new roles show up as a notice.
CREATE TABLE company_follows (
  user_id    BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  company_id BIGINT NOT NULL REFERENCES companies(id) ON DELETE CASCADE,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (user_id, company_id)
);
CREATE INDEX company_follows_company ON company_follows (company_id);

ALTER TABLE notifications DROP CONSTRAINT notifications_kind_check;
ALTER TABLE notifications ADD CONSTRAINT notifications_kind_check
  CHECK (kind IN ('strong_matches', 'posting_closed', 'reminder', 'followed_company'));

-- +goose Down
DELETE FROM notifications WHERE kind = 'followed_company';
ALTER TABLE notifications DROP CONSTRAINT notifications_kind_check;
ALTER TABLE notifications ADD CONSTRAINT notifications_kind_check
  CHECK (kind IN ('strong_matches', 'posting_closed', 'reminder'));
DROP TABLE company_follows;
