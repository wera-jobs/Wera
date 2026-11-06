# Storage

How much the database holds, what grows it, and the limits that keep it in
check. Measured on the home server on 5 November 2026, with 9 users (7 with a
profile).

## Before the limits

The database was 7.3 GB:

| Table | Size | Rows | Grows with |
|---|---|---|---|
| jobs | 4.5 GB (3.2 GB of it description text) | 911,000, of which 337,000 closed | every posting ever found, about 140,000 a week |
| user_jobs | 2.3 GB | 6.3 million: about 910,000 per user, of which 98-99% rule rejections | users × every posting ever found |
| job_embeddings | 191 MB | 42,000 | postings someone matches |
| analyses, job_facts | 73 MB, 62 MB | 46,000, 42,000 | scores; matched postings |

Each user cost about 325 MB, nearly all of it rows saying "this posting does
not fit", and gained about 50 MB a week even when idle. At 1,000 users that
would have been about 500 GB a month after launch and 1.7 TB after six
months, on a 457 GB disk.

## The limits

**Only matches are stored per user.** A posting the rules reject leaves no
`user_jobs` row. Instead, `jobs.changed_at` moves when a posting is added,
changes or is listed again after closing, and `profiles.filtered_through` is
when the user's last filter pass started; a pass reads only postings changed
since then (with a 10-minute overlap for postings saved during the last
pass). A preference change still reads every open posting. Rows marked
`excluded` remain only for jobs the AI ruled out, which keep the analysis so
a later refilter links it for free, and jobs the user tracks. Migration 0034
deleted the 6.26 million rejection rows; a user now costs about 2 to 9 MB
(their matches, scores and grades), growing with matches.

**Closed postings are deleted after 90 days**, with their facts, embeddings,
grades and per-user rows, unless a user tracks the posting or wrote a cover
letter or resume for it. One that is listed again is fetched as new.
Embeddings and quick grades of closed postings go after 30 days: they only
rank open postings.

**Superseded scores are deleted 30 days after the newer score.** A profile
edit rescores jobs; the old score is kept a month so the digest can still
tell a rescored job from a new match, and kept for good on tracked jobs.

Pruning runs in each pipeline run's housekeeping (`store.Prune`), in batches
of 5,000 rows, for at most 5 minutes; the next run continues.

## Expected size

At 1,000 users the database should be about 25 to 40 GB: open postings
(about 5 GB with their facts and embeddings), about 20,000 matches per user
(about 7 GB), current scores (about 100 a day per user at 1.3 KB, held to
the live set by the limits above) and the AI usage log, which is kept for
billing (about 170 bytes a call, about 6 GB a year).

## Backups

`deploy/home/backup.sh --daily` runs from cron: a full dump (about 2 GB
compressed before the limits), then rotation (every backup from the last 7
days, then one a week, none past 29 days, as the privacy policy promises),
then an optional copy to an S3-compatible bucket. See
[deploy/home/README.md](../deploy/home/README.md#backups).
