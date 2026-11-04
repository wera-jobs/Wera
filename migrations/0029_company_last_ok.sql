-- +goose Up
-- When each board was last read successfully. A board that cannot be read
-- for a week (the company moved to another job system, took its board
-- down, or was disabled) has its open postings closed, since nothing else
-- would ever close them. Boards failing now get the last time one of their
-- postings was seen, so none is closed sooner than a week after that.
ALTER TABLE companies ADD COLUMN last_ok_at TIMESTAMPTZ;
UPDATE companies c
SET last_ok_at = CASE WHEN c.last_fetch_ok THEN c.last_fetch_at
                      ELSE (SELECT max(j.last_seen_at) FROM jobs j WHERE j.company_id = c.id AND j.source <> 'manual') END;

-- +goose Down
ALTER TABLE companies DROP COLUMN last_ok_at;
