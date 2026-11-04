# Quick grades

A full fit score (one AI call per job, with a written reason) costs about
$0.00012 and is limited to `SCORES_PER_DAY` per user, 100 by default. A
user's queue holds 1,000 to 17,000 matches, so most of it used to be ranked
only by the local estimate: embeddings, word and skill overlap, and a model
fit to the user's own scores.

Quick grades fill that gap. One call grades 20 jobs from their shared
facts (title, company, location, seniority, years, work mode, skills and
the posting's digest). The model returns only the five 0-10 ratings and the
blocker flag the full score uses, and the score is computed from them in
code exactly as a full score is (`Ratings.Score`). There is no reason
sentence, which is most of a full score's output and cost.

## Where they fit

Each scoring pass for a user (after a profile save and on every scheduled
run):

1. extracts missing shared facts (up to half the run's budget);
2. drops jobs the user's rules exclude on those facts;
3. **grades every remaining job without a grade**, best estimate first,
   with up to half of what the facts left;
4. ranks the queue, the grades replacing the local estimate of each graded
   job, and gives full scores to the best within the daily limit;
5. keeps grades until the profile text changes (they are stored per
   scoring key, like full scores).

Grades cost the user's budget (`llm_usage.kind = 'quick'`) but not the daily
score limit. The dashboard shows a graded job's estimate as "AI estimate",
and Today shows grading progress from `/api/today`'s `ungraded` count.

## How well they agree with full scores

400 jobs per user that already had a full score, graded again in batches of
20. Rank correlation (Spearman) with the full score, measured 2026-11-04:

| User | Local estimate | Quick grade, first 45 words of the digest | Quick grade, whole digest |
|---|---|---|---|
| A | 0.73 | 0.84 | |
| B | 0.18 | 0.88 | |
| C | 0.05 | 0.72 | 0.82 |
| D | 0.24 | 0.61 | 0.80 |

The whole digest is what ships: it cost 10% more and lifted the weakest
users the most. Of the jobs in a user's top quarter by full score, the quick
grades put 76% to 88% in their own top quarter (the local estimate: about
30%). Three in four grades land in the same verdict band as the full score.

Cost per graded job: $0.000035 to $0.000039 (about 200 prompt tokens and 18
output tokens per job), a third of a full score.

## Measured on a copy of live data

A new user whose preferences match 16,600 open jobs: the first scoring pass
graded 2,380 jobs and gave 100 full scores in 56 seconds, for $0.14, with a
$0.20 cap for the test (the real per-run cap is $1, enough to grade the
whole queue in one pass).

## Setup filters early

A new user's first filter pass writes a row for each of about 570,000 open
jobs. It used to run inside the final profile save: 91 seconds on a copy of
live data, past the request's 45-second limit, so the save gave up and new
users met an empty Today. Now the setup wizard sends the preferences when
the user leaves that step (`PUT /api/profile/preferences`), and the filter
runs while the profile text is drafted and reviewed: 43 seconds, in the
background. The save then waits about 16 seconds, mostly ranking. New rows
are copied straight into `user_jobs`, and exclusions and score links are
written in batches rather than one statement each.
