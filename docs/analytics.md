# Site analytics

The admin app's Traffic page shows who visits the dashboard and the
landing page, and how much the API is asked. It needs no cookie banner.

## What is collected

The dashboard sends a small beacon (`POST /api/page-seen`) when a page is
seen and when it is left (with how long it was in view). It stores nothing
in the browser, and sends nothing when the browser asks not to be tracked
(Do Not Track, Global Privacy Control).

The server keeps, per event: the path (no query, ids replaced by `:id`),
the referring site's host, UTM tags, device kind, browser, system,
country, and a visitor hash. The hash is SHA-256 of the day's random salt,
the address and the browser string; the salt is deleted the next day, so
nobody can tell who a visitor was or follow them across days. No address,
browser string or account is stored. Crawlers are left out. Events are
deleted after 13 months (`store.Prune`).

## Numbers

- **Visitors**: unique visitors per day. A range adds the days up, so a
  person who came on two days counts twice.
- **Visit**: a visitor's pages until 30 minutes pass without one.
- **Bounce rate**: visits that saw one page for under 10 seconds.
- **Visit time**: time pages were in view during a visit.
- **Requests**: every API request but health checks, per day, route and
  method, with 4xx and 5xx errors and average time. Counted in memory and
  written about once a minute.
