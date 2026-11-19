-- +goose Up
-- Every status an application takes, with when, for the progress view
-- (funnel, response rates, time between stages). A trigger records them,
-- so every writer is covered.
CREATE TABLE application_changes (
  user_id BIGINT NOT NULL,
  job_id  BIGINT NOT NULL,
  status  TEXT NOT NULL,
  at      TIMESTAMPTZ NOT NULL DEFAULT now(),
  FOREIGN KEY (user_id, job_id) REFERENCES applications (user_id, job_id) ON DELETE CASCADE
);
CREATE INDEX application_changes_user ON application_changes (user_id, job_id, at);

-- +goose StatementBegin
CREATE FUNCTION record_application_change() RETURNS trigger AS $$
BEGIN
  IF TG_OP = 'INSERT' OR NEW.status IS DISTINCT FROM OLD.status THEN
    INSERT INTO application_changes (user_id, job_id, status, at) VALUES (NEW.user_id, NEW.job_id, NEW.status, now());
  END IF;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER application_changes_trg AFTER INSERT OR UPDATE OF status ON applications
  FOR EACH ROW EXECUTE FUNCTION record_application_change();

-- What is known of the past: the first application, then the current status.
INSERT INTO application_changes (user_id, job_id, status, at)
SELECT user_id, job_id, 'applied', applied_at FROM applications
WHERE applied_at IS NOT NULL AND status <> 'applied';
INSERT INTO application_changes (user_id, job_id, status, at)
SELECT user_id, job_id, status, CASE WHEN status = 'applied' THEN coalesce(applied_at, updated_at) ELSE updated_at END FROM applications;

-- +goose Down
DROP TRIGGER application_changes_trg ON applications;
DROP FUNCTION record_application_change();
DROP TABLE application_changes;
