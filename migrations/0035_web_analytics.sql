-- +goose Up
-- Site analytics without cookies or consent: one row per page seen and per
-- page left (how long it was in view). A visitor is a hash of the day's
-- salt, the address and the browser: it counts unique visitors within one
-- day and cannot be traced back once the salt is deleted the next day. No
-- address, user agent or account is stored.
CREATE TABLE web_events (
  id          BIGSERIAL PRIMARY KEY,
  at          TIMESTAMPTZ NOT NULL DEFAULT now(),
  kind        TEXT NOT NULL CHECK (kind IN ('view', 'leave')),
  visitor     TEXT NOT NULL,
  path        TEXT NOT NULL,
  referrer    TEXT NOT NULL DEFAULT '',
  utm_source  TEXT NOT NULL DEFAULT '',
  utm_medium  TEXT NOT NULL DEFAULT '',
  utm_campaign TEXT NOT NULL DEFAULT '',
  device      TEXT NOT NULL DEFAULT '',
  browser     TEXT NOT NULL DEFAULT '',
  os          TEXT NOT NULL DEFAULT '',
  country     TEXT NOT NULL DEFAULT '',
  engaged_ms  INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX web_events_at_idx ON web_events (at);

-- The day's salt for visitor hashes; yesterday's is deleted.
CREATE TABLE analytics_salts (
  day  DATE PRIMARY KEY,
  salt BYTEA NOT NULL
);

-- Requests the API answered, per day, route and method.
CREATE TABLE api_requests_daily (
  day        DATE NOT NULL,
  route      TEXT NOT NULL,
  method     TEXT NOT NULL,
  requests   BIGINT NOT NULL DEFAULT 0,
  errors_4xx BIGINT NOT NULL DEFAULT 0,
  errors_5xx BIGINT NOT NULL DEFAULT 0,
  total_ms   BIGINT NOT NULL DEFAULT 0,
  PRIMARY KEY (day, route, method)
);

-- +goose Down
DROP TABLE api_requests_daily;
DROP TABLE analytics_salts;
DROP TABLE web_events;
