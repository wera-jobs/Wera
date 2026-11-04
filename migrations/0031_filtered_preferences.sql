-- +goose Up
-- The preferences a user's jobs were last filtered with. A profile save
-- that changes the preferences no longer marks every one of the user's jobs
-- (hundreds of thousands of rows); the next filter pass sees the difference,
-- filters every open job again and writes only the outcomes that changed.
ALTER TABLE profiles ADD COLUMN filtered_preferences JSONB;
UPDATE profiles SET filtered_preferences = preferences;

-- +goose Down
ALTER TABLE profiles DROP COLUMN filtered_preferences;
