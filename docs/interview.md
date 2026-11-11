# Interview practice

The dashboard's Interview page has two kinds of practice:

- **Question rounds**: multiple-choice questions by domain and difficulty
  (`interview_questions`, answers in `interview_attempts`).
- **Work simulations**: a few days at Wera told as emails, each with four
  replies to choose from. Each reply is rated best, okay or weak, with
  feedback on why. These work like the situational rounds in big-tech
  interview loops.

## Tracks: by role, with the stack as a filter

A track is a role: Backend, Frontend, Full-stack, Data & ML, DevOps & SRE,
Mobile, Product & program, and Any role (`internal/interview/tracks.go`).

- **Why role first:** an interview loop is shaped by the role. A backend
  loop covers system design and databases, a frontend loop covers the
  browser and accessibility, and a PM loop covers product sense.
- **What a track picks:** its domains, in the order the loop weighs them,
  and its simulations.
- **Where the stack fits:** the stack (Go, React, Kubernetes, ...) mostly
  changes the language of the coding round, which multiple choice can't
  test well. So the stack is a finer filter: questions carry `tags`, and a
  round with `stack=go,postgres` puts tagged questions first, after the
  ones the user hasn't answered or got wrong last time.
- **The suggestion:** `GET /api/interview/tracks` suggests a track from the
  profile's role families. Roles outside tech get Any role, whose practice
  is behavioral questions and the everyday-workplace simulation.

## Simulations

Simulations ship in the binary (`internal/interview/scenarios.json`), so
editing one needs no migration.

- **Load-time checks:** each step has exactly one best reply, four replies
  in all, feedback on every reply, and a takeaway. Every track has at least
  one simulation.
- **Answers stay hidden:** the API serves a simulation without ratings or
  feedback. A step's ratings come back only once it is answered.
- **Storage:** replies go in `interview_scenario_answers`. Answering step 0
  starts a new run. A simulation's best score is its best finished run
  (2 points for best, 1 for okay, 0 for weak).
- **Writing new ones:** keep the best reply's position varied (a test
  checks this), use the shared cast at Wera, and make the okay replies
  plausible: right instinct, missing a piece.

## API

| Method | Path | What |
| --- | --- | --- |
| GET | `/api/interview/tracks` | tracks with domains, stack tags (with counts), simulation ids, and `suggested` |
| GET | `/api/interview/domains?tz=` | progress by domain, plus `practice`: day streak, answers this week, questions last answered wrong |
| GET | `/api/interview/quiz?track=&domain=&difficulty=&stack=&missed=1&limit=` | a round; `missed=1` only draws questions last answered wrong |
| POST | `/api/interview/questions/{id}/answer` | `{"choice": 0-3}`, reveals the answer and explanation |
| GET | `/api/interview/scenarios` | every simulation with the user's progress |
| GET | `/api/interview/scenarios/{id}` | its emails and replies, plus the steps already answered on the current run, revealed |
| POST | `/api/interview/scenarios/{id}/steps/{step}/answer` | `{"choice": 0-3}`, reveals every reply's rating and feedback, and the takeaway |
