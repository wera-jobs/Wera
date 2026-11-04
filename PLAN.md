# Wera: Backend Plan

> **Wera** (Swahili slang for job/gig) collects entry-level infrastructure jobs from public job-board APIs, filters them, scores each one against my resume with an LLM on Coral Bricks, and serves the results to a dashboard. It never applies on my behalf. I review and apply myself.

**Agent instructions:** Read this whole file before writing code. Build one milestone at a time, in order. Do not start the next milestone until the current one's acceptance checks pass. Ask before adding dependencies not listed here.

---

## 1. Goals and non-goals

**Goals**
- Pull jobs from **official public job-board APIs** (Greenhouse, Lever, Ashby) for a configurable list of companies.
- Keep only **US / US-remote, entry-level infrastructure roles**: SRE, platform, DevOps, infrastructure, production engineering, trading operations / trade support, systems, MLOps / AI infrastructure.
- **Remove jobs that explicitly say they will not sponsor visas.**
- Score every remaining job against my resume with an LLM and store a structured analysis.
- Expose everything over a REST API for the dashboard (built later).
- Run unattended on a schedule, first on my Ubuntu laptop, then on my Ubuntu server.
- Track token usage and cost per call so I can show real numbers.

**Non-goals**
- No auto-applying, no form filling, no login-protected sources.
- **No LinkedIn, Indeed, or any HTML scraping.** Public ATS JSON APIs only.
- No multi-user support (single user, private deployment).

**Scalability rule:** adding a company or a role type must require **only a YAML edit**, never a code change.

---

## 2. Architecture

```
          ┌────────────────────────── wera worker (every 30 min) ─────────────────────────┐
          │                                                                               │
config/   │  1. FETCH            2. NORMALIZE      3. RULE FILTER       4. LLM SCORE      │
companies │  greenhouse/lever/ → common Job    → title/location/   → Coral Bricks       │
.yaml ───►│  ashby adapters      struct, dedupe    seniority/visa      (new jobs only,   │
roles.yaml│  (concurrent,        by (source,       (free, no LLM)      parallel, cached  │
profile/  │   polite)            ext_id)                               profile prefix)   │
          │                                                                               │
          └──────────────────────────────────┬────────────────────────────────────────────┘
                                             ▼
                                       PostgreSQL 16
                                             ▲
                             wera api (REST + /metrics + /healthz)
                                             ▲
                                   dashboard (later, React)
```

One Go binary, `wera`, with subcommands. The **worker** and **API** run as separate processes sharing Postgres.

---

## 3. Tech stack

| Concern | Choice |
|---|---|
| Language | Go 1.23+ |
| DB | PostgreSQL 16 (Docker locally) |
| DB driver | `github.com/jackc/pgx/v5` (pgxpool) |
| Migrations | `github.com/pressly/goose/v3` with embedded SQL files |
| HTTP router | `github.com/go-chi/chi/v5` |
| Config | `gopkg.in/yaml.v3` + env vars (`github.com/joho/godotenv` for local `.env`) |
| LLM client | Plain `net/http` against the OpenAI-compatible endpoint (no SDK needed) |
| Metrics | `github.com/prometheus/client_golang` |
| Logging | stdlib `log/slog` (JSON in prod, text locally) |
| Concurrency | `golang.org/x/sync/errgroup` + `semaphore` |
| Tests | stdlib `testing` + `net/http/httptest` |

---

## 4. Repository layout

```
wera/
├── PLAN.md
├── README.md
├── .gitignore                 # .env, profile/*.md (except example), bin/, *.local.yaml
├── .env.example
├── go.mod
├── Makefile
├── docker-compose.yml         # postgres (+ worker/api later)
├── Dockerfile
├── cmd/wera/main.go           # subcommand dispatch
├── config/
│   ├── companies.yaml         # WHO to watch
│   └── roles.yaml             # WHAT counts as a match
├── profile/
│   ├── profile.example.md     # committed template
│   └── profile.md             # my resume + preferences (gitignored)
├── migrations/                # goose SQL, embedded via embed.FS
│   └── 0001_init.sql
└── internal/
    ├── config/                # load + validate YAML and env
    ├── sources/
    │   ├── source.go          # Source interface + registry
    │   ├── greenhouse/        # adapter + fixtures + tests
    │   ├── lever/
    │   └── ashby/
    ├── normalize/             # HTML→text, location parsing, content hash
    ├── filter/                # rule engine driven by roles.yaml
    ├── scoring/               # Coral client, prompt, JSON schema, cost accounting
    ├── store/                 # pgx repository functions
    ├── pipeline/              # orchestrates fetch → filter → score, run records, advisory lock
    ├── api/                   # chi handlers
    └── metrics/               # Prometheus collectors
```

---

## 5. Configuration

### 5.1 `.env` (never committed)

```bash
DATABASE_URL=postgres://wera:wera@localhost:5432/wera?sslmode=disable
CORAL_API_KEY=cb_xxx                       # never log this, never commit this
CORAL_BASE_URL=https://inference.coralbricks.ai/v1
CORAL_MODEL=glm-5.3-flash-fast
CORAL_DEEP_MODEL=glm-5.3-fast              # used only for optional deep review (Milestone 7)
SCORING_CONCURRENCY=12
FETCH_CONCURRENCY=6
RUN_INTERVAL=30m
HTTP_ADDR=:8080
LOG_FORMAT=text                            # json on the server
USER_AGENT=Wera/0.1 (personal job tracker; contact: jessechumo@gmail.com)
```

**Prices** (USD per 1M tokens), stored in code as a map so cost is computed per call. **Cached input reads are free.**

| Model | Input | Cache write | Output |
|---|---|---|---|
| `glm-5.3-flash-fast` | 0.15 | 0.23 | 0.50 |
| `glm-5.3-fast` | 1.12 | 1.68 | 4.40 |
| `deepseek-v4.1-flash-fast` | 0.30 | 0.09 | 1.20 |

### 5.2 `config/companies.yaml`

Each entry: `name`, `ats` (`greenhouse` | `lever` | `ashby`), `token` (board slug), `industry` (an id from `config/industries.yaml`; this replaced the original `group` of `ai_infra` | `trading` | `other`), `enabled`, optional `notes`.

Tokens marked **verified** were confirmed to return a valid job list. The rest must be checked with `wera discover` (Milestone 2) before enabling.

```yaml
companies:
  # ---------- AI inference / AI infrastructure ----------
  - { name: Baseten,        ats: ashby,      token: baseten,          group: ai_infra, enabled: true }   # verified
  - { name: Modal,          ats: ashby,      token: modal,            group: ai_infra, enabled: true }   # verified
  - { name: Lambda,         ats: ashby,      token: lambda,           group: ai_infra, enabled: true }   # verified
  - { name: Together AI,    ats: greenhouse, token: togetherai,       group: ai_infra, enabled: true }   # verified
  - { name: CoreWeave,      ats: greenhouse, token: coreweave,        group: ai_infra, enabled: true }   # verified
  - { name: Fireworks AI,   ats: greenhouse, token: fireworksai,      group: ai_infra, enabled: false, notes: "404 on greenhouse; try ashby/lever" }
  - { name: Groq,           ats: greenhouse, token: groq,             group: ai_infra, enabled: false, notes: "404 on greenhouse; try others" }
  - { name: Anyscale,       ats: greenhouse, token: anyscale,         group: ai_infra, enabled: false }
  - { name: Replicate,      ats: ashby,      token: replicate,        group: ai_infra, enabled: false }
  - { name: Cerebras,       ats: greenhouse, token: cerebrassystems,  group: ai_infra, enabled: false }
  - { name: Crusoe,         ats: ashby,      token: crusoe,           group: ai_infra, enabled: false }
  - { name: Nebius,         ats: greenhouse, token: nebius,           group: ai_infra, enabled: false }
  - { name: RunPod,         ats: ashby,      token: runpod,           group: ai_infra, enabled: false }

  # ---------- Trading firms / exchanges ----------
  - { name: DRW,                 ats: greenhouse, token: drweng,           group: trading, enabled: true }  # verified
  - { name: Akuna Capital,       ats: greenhouse, token: akunacapital,     group: trading, enabled: true }  # verified
  - { name: Hudson River Trading,ats: greenhouse, token: wehrtyou,         group: trading, enabled: true }  # verified
  - { name: Optiver,             ats: greenhouse, token: optiverus,        group: trading, enabled: true }  # verified
  - { name: IMC Trading,         ats: greenhouse, token: imc,              group: trading, enabled: true }  # verified
  - { name: Belvedere Trading,   ats: lever,      token: belvederetrading, group: trading, enabled: true }  # verified
  - { name: Wolverine Trading,   ats: greenhouse, token: wolverinetrading, group: trading, enabled: false, notes: "404; find real board" }
  - { name: PEAK6,               ats: greenhouse, token: peak6,            group: trading, enabled: false }
  - { name: Jump Trading,        ats: greenhouse, token: jumptrading,      group: trading, enabled: false }
  - { name: Chicago Trading Co,  ats: greenhouse, token: chicagotrading,   group: trading, enabled: false }
  - { name: Old Mission,         ats: greenhouse, token: oldmissioncapital,group: trading, enabled: false }
  - { name: Tower Research,      ats: greenhouse, token: towerresearchcapital, group: trading, enabled: false }
  - { name: Two Sigma,           ats: greenhouse, token: twosigma,         group: trading, enabled: false }
```

Companies on custom career sites (Citadel, Jane Street, CME, Cboe, etc.) are out of scope until a public API is found. Do not scrape them.

### 5.3 `config/roles.yaml`

All matching logic lives here. Matching is case-insensitive. Patterns are Go regular expressions.

**Go uses RE2: no lookahead/lookbehind.** Write patterns without them.

```yaml
title_overrides:            # checked FIRST; a match skips exclude_title (still subject to location/sponsorship)
  # AI startups use "Member of Technical Staff" as a generic title; "staff" here is not seniority.
  mts_infra: ['\bmember of technical staff\b.*\b(systems|infrastructure|infra|inference|platform|reliability|sre|devops|cloud|gpu)\b']

role_categories:            # a job must match at least one include pattern
  sre:            ['\bsite reliability\b', '\bSRE\b', '\breliability engineer']
  platform:       ['\bplatform engineer', '\bcloud platform\b', '\binfrastructure platform']
  devops:         ['\bdevops\b', '\bbuild (and|&) release\b', '\brelease engineer']
  infrastructure: ['\binfrastructure engineer', '\binfrastructure software engineer', '\bcloud engineer', '\bsystems? engineer', '\blinux\b']
  production:     ['\bproduction engineer', '\bproduction support\b']
  trading_ops:    ['\btrad(e|ing) (operations|ops|support|systems)', '\btrade systems engineer', '\bapplication support engineer']
  ml_infra:       ['\bml infrastructure\b', '\bmlops\b', '\bai infrastructure\b', '\binference\b', '\bml platform\b', '\bgpu (infrastructure|platform|cloud)\b']
  swe_infra:      ['\bsoftware engineer\b.*\b(infra|infrastructure|platform|reliability|systems|cloud|devops|observability)\b',
                   '\b(infra|infrastructure|platform|observability)\b.*\bsoftware engineer\b']
  early_career:   ['\bnew grad', '\bearly career\b', '\buniversity grad', '\bgraduate (software|systems|infrastructure)']

exclude_title:              # drop immediately
  - '\bsenior\b'
  - '\bsr\.?\b'
  - '\bstaff\b'
  - '\bprincipal\b'
  - '\blead\b'
  - '\bmanager\b'
  - '\bdirector\b'
  - '\bhead of\b'
  - '\bvp\b'
  - '\bintern(ship)?\b'
  - '\bco-?op\b'
  - '\bIII\b'
  - '\bIV\b'

seniority:
  max_years_required: 3     # LLM-extracted; jobs requiring more are excluded

location:
  allow_countries: ['US']
  allow_remote_us: true
  # rule layer keeps a job if location text contains a US state/city/"United States"/"Remote - US"/"USA",
  # or is ambiguous ("Remote"); the LLM then confirms. Clearly non-US (London, Bangalore, Singapore...) is dropped.

sponsorship:
  # Explicit refusal = hard exclude. Matched against the full plain-text description.
  # These were tested against real posting sentences (see the test table below). Note the YAML
  # single-quote escaping: an apostrophe inside a pattern is written as ''.
  exclude_patterns:
    - '\b(will|do|does|can) ?not (be )?(provid|offer|sponsor)\w*[^.]{0,60}(sponsor|visa)'
    - '\b(won''t|cannot|can''t|unable to|not able to) (be )?(provid|offer|sponsor)\w*[^.]{0,60}(sponsor|visa)'
    - '\b(do|does|will|can) ?not sponsor\b'
    - '\bno (visa |employment )?sponsorship\b'
    - '\bsponsorship (is |will )?not (be )?(available|provided|offered)'
    - '\bwithout (a |the |any )?(need for |requirement for |the need for )?(current or future |current |future |present or future )?(visa |employment |immigration )?sponsorship'
    - '\bnot (be )?(able|eligible) to sponsor'
    - '\bU\.?S\.? citizens? only\b'
    - '\bmust be a U\.?S\.? citizen'
    - '\bactive (security )?clearance (is )?required'
  # ITAR "U.S. person" requirements are flagged (not auto-excluded) and surfaced for manual review.
  flag_patterns:
    - 'ITAR'
    - 'U\.?S\.? persons?'
    - 'export control'
```

### 5.4 `profile/profile.md` (gitignored)

Plain markdown: my full resume text, target role categories, preferences (open to relocation anywhere in the US, prefers remote/hybrid but onsite is fine, needs future H-1B sponsorship, currently on OPT), and strengths to weigh (Go, Linux/RHEL production, Ansible, Python, Kubernetes/Terraform/Prometheus project, ML/LLM infrastructure, research). The scorer loads this file verbatim as the cached prefix. **If this file changes, its SHA-256 changes and the cache key changes.**

---

## 6. Data model (`migrations/0001_init.sql`)

```sql
CREATE TABLE companies (
  id          BIGSERIAL PRIMARY KEY,
  name        TEXT NOT NULL UNIQUE,
  ats         TEXT NOT NULL CHECK (ats IN ('greenhouse','lever','ashby')),
  token       TEXT NOT NULL,
  grp         TEXT NOT NULL,
  enabled     BOOLEAN NOT NULL DEFAULT TRUE,
  last_fetch_at     TIMESTAMPTZ,
  last_fetch_ok     BOOLEAN,
  last_fetch_error  TEXT,
  UNIQUE (ats, token)
);

CREATE TABLE jobs (
  id              BIGSERIAL PRIMARY KEY,
  company_id      BIGINT NOT NULL REFERENCES companies(id),
  source          TEXT NOT NULL,              -- greenhouse|lever|ashby
  ext_id          TEXT NOT NULL,              -- id in the source system
  title           TEXT NOT NULL,
  location_raw    TEXT,
  is_remote       BOOLEAN,
  url             TEXT NOT NULL,
  department      TEXT,
  description     TEXT,                       -- plain text, HTML stripped
  content_hash    TEXT NOT NULL,              -- sha256(title+location+description)
  posted_at       TIMESTAMPTZ,                -- from source when available
  first_seen_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
  last_seen_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  closed_at       TIMESTAMPTZ,                -- set when it disappears from the feed
  -- rule-filter outcome
  stage           TEXT NOT NULL DEFAULT 'new' -- new|excluded|pending_score|scored|score_failed
                  CHECK (stage IN ('new','excluded','pending_score','scored','score_failed')),
  matched_categories TEXT[] NOT NULL DEFAULT '{}',
  exclude_reason  TEXT,                       -- e.g. 'title:senior', 'location:non_us', 'sponsorship:explicit_no', 'llm:years>3'
  exclude_evidence TEXT,                      -- the matched sentence, for auditing filters
  flags           TEXT[] NOT NULL DEFAULT '{}', -- e.g. {'itar'}
  UNIQUE (source, ext_id)
);
CREATE INDEX jobs_stage_idx ON jobs (stage);
CREATE INDEX jobs_first_seen_idx ON jobs (first_seen_at DESC);

CREATE TABLE analyses (
  id               BIGSERIAL PRIMARY KEY,
  job_id           BIGINT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
  kind             TEXT NOT NULL DEFAULT 'score' CHECK (kind IN ('score','deep')),
  model            TEXT NOT NULL,
  profile_hash     TEXT NOT NULL,
  fit_score        INT CHECK (fit_score BETWEEN 0 AND 100),
  verdict          TEXT,                       -- strong|good|stretch|poor
  seniority        TEXT,                       -- entry|junior|mid|senior|unknown
  years_required   INT,
  sponsorship      TEXT,                       -- yes|no|unknown
  sponsorship_quote TEXT,
  work_mode        TEXT,                       -- remote|hybrid|onsite|unknown
  us_eligible      BOOLEAN,
  location_summary TEXT,
  skills_matched   TEXT[],
  skills_missing   TEXT[],
  reason           TEXT,                       -- one or two sentences
  raw              JSONB NOT NULL,             -- full model JSON
  prompt_tokens    INT, cached_tokens INT, completion_tokens INT,
  cost_usd         NUMERIC(12,6),
  latency_ms       INT,
  created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (job_id, kind, profile_hash)
);

CREATE TABLE applications (                    -- written by the dashboard; I apply manually
  job_id      BIGINT PRIMARY KEY REFERENCES jobs(id) ON DELETE CASCADE,
  status      TEXT NOT NULL DEFAULT 'saved'
              CHECK (status IN ('saved','applied','interviewing','offer','rejected','not_interested')),
  notes       TEXT,
  applied_at  TIMESTAMPTZ,
  updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE runs (
  id              BIGSERIAL PRIMARY KEY,
  started_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
  finished_at     TIMESTAMPTZ,
  status          TEXT NOT NULL DEFAULT 'running' CHECK (status IN ('running','ok','partial','failed')),
  companies_ok    INT DEFAULT 0,
  companies_failed INT DEFAULT 0,
  jobs_seen       INT DEFAULT 0,
  jobs_new        INT DEFAULT 0,
  jobs_excluded   INT DEFAULT 0,
  jobs_scored     INT DEFAULT 0,
  prompt_tokens   BIGINT DEFAULT 0, cached_tokens BIGINT DEFAULT 0, completion_tokens BIGINT DEFAULT 0,
  cost_usd        NUMERIC(12,6) DEFAULT 0,
  error           TEXT
);
```

**"Removed" means excluded, not deleted.** Excluded jobs stay in the table with `exclude_reason` and `exclude_evidence` so I can audit and tune the filters. The API hides them by default.

---

## 7. Pipeline in detail

`wera pipeline` runs once. `wera worker` runs it at each `RUN_SCHEDULE` time (e.g. `09:00,11:00,...,19:00` in `RUN_TIMEZONE`), or loops it every `RUN_INTERVAL` when no schedule is set. Each run:

1. **Acquire a Postgres advisory lock** (`pg_try_advisory_lock(4242)`). If held, log and exit: no overlapping runs.
2. **Insert a `runs` row.** Sync `companies.yaml` into the `companies` table (upsert by name).

### 7.1 Fetch (`internal/sources`)

```go
type Source interface {
    Name() string                                   // "greenhouse"
    Fetch(ctx context.Context, token string) ([]RawJob, error)
}
type RawJob struct {
    ExtID, Title, LocationRaw, URL, Department, DescriptionHTML, DescriptionText string
    IsRemote *bool
    PostedAt *time.Time
}
```

| ATS | Endpoint | Key fields |
|---|---|---|
| Greenhouse | `GET https://boards-api.greenhouse.io/v1/boards/{token}/jobs?content=true` | `jobs[].id`, `title`, `location.name`, `absolute_url`, `first_published`, `updated_at`, `content` (**HTML-escaped HTML, unescape then strip**), `departments[].name` |
| Lever | `GET https://api.lever.co/v0/postings/{token}?mode=json` | `id`, `text` (title), `categories.location`, `categories.team`, `workplaceType`, `hostedUrl`, `createdAt` (**epoch ms**), `descriptionPlain`, `lists[].text/content`, `additionalPlain` |
| Ashby | `GET https://api.ashbyhq.com/posting-api/job-board/{token}?includeCompensation=true` | `jobs[].id`, `title`, `location`, `secondaryLocations`, `isRemote`, `workplaceType`, `publishedAt`, `jobUrl`, `descriptionPlain`, `isListed` (**skip if false**) |

Rules for every adapter:
- Shared `http.Client` with **15s timeout**, the `USER_AGENT` header, and gzip.
- Retry 429/5xx up to 3 times with exponential backoff + jitter. Never retry 404 (record `last_fetch_error = "404 token not found"`).
- `FETCH_CONCURRENCY` companies at once (errgroup + semaphore). **One company failing never fails the run.**
- Each adapter has a `testdata/*.json` fixture and a table-driven parse test.

### 7.2 Normalize (`internal/normalize`)

- HTML → plain text (strip tags, collapse whitespace, keep paragraph breaks).
- `content_hash = sha256(lower(title)|location|description)`.
- Upsert by `(source, ext_id)`:
  - new row → `stage='new'`, count as `jobs_new`
  - existing row → update `last_seen_at`; if `content_hash` changed, update content and reset `stage='new'` so it is re-filtered and re-scored
- Jobs for a company that fetched **successfully** but no longer appear → set `closed_at`. Never close jobs for a company whose fetch failed.

### 7.3 Rule filter (`internal/filter`, no LLM, applied to `stage='new'`)

Order matters; the first failing check sets `stage='excluded'` with reason and evidence:

0. `title_overrides` match → skip steps 1–2, record the override name as the category
1. `exclude_title` match → `title:<pattern>`
2. No `role_categories` match on the title → `title:no_category`
3. Clearly non-US location → `location:non_us`
4. Description matches a `sponsorship.exclude_patterns` → `sponsorship:explicit_no` (store the matched sentence)
5. Otherwise → `stage='pending_score'`, set `matched_categories`, and add `itar` to `flags` if a `flag_patterns` matches.

The filter is pure (no DB) and fully table-tested. The **title** test table must include at least:

| Title | Expected |
|---|---|
| Trade Systems Engineer | keep (trading_ops) |
| Site Reliability Engineer | keep (sre) |
| Senior Site Reliability Engineer | exclude (title:senior) |
| SRE Monitoring Platform Software Engineer (Entry Level) | keep |
| Infrastructure Software Engineer | keep |
| Software Engineer - Dedicated Inference | keep (ml_infra) |
| Software Engineer, Infrastructure | keep (swe_infra) |
| Member of Technical Staff - Systems | keep (override mts_infra) |
| Member of Technical Staff - Product Design | exclude |
| Staff Software Engineer, Infrastructure | exclude (title:staff) |
| Junior Cloud Platform Engineer | keep (platform) |
| Software Engineer Intern, Infrastructure | exclude (title:intern) |
| Software Engineer, Frontend | exclude (title:no_category) |
| Account Executive - AI Native | exclude (title:no_category) |

The sponsorship test table **must include at least these cases**:

| Sentence | Expected |
|---|---|
| IBM will not be providing visa sponsorship for this position now or in the future. | exclude |
| Swing Education is unable to provide employment visa sponsorship, including H-1B sponsorship, for this position. | exclude |
| Applicants must be currently authorized to work in the United States on a full-time basis without current or future sponsorship. | exclude |
| We are not able to sponsor visas for this role. | exclude |
| Sponsorship is not available for this position. | exclude |
| Candidates must have the ability to work without a need for current or future visa sponsorship. | exclude |
| We do not sponsor visas. | exclude |
| The company cannot sponsor employment visas at this time. | exclude |
| No visa sponsorship is available. | exclude |
| Sponsorship is available for this role. | keep |
| Visa sponsorship (including H-1B) may be available for this position. | keep |
| We offer visa sponsorship. | keep |
| Eligible for visa sponsorship and open to candidates with OPT/CPT. | keep |
| There is no requirement to relocate. | keep |
| We provide sponsorship for qualified candidates without exception. | keep |
| Sponsorship is not required to apply; we sponsor visas. | keep |

Indirect phrasings the rules miss (e.g. *"does not intend to hire candidates who will need sponsorship"*) are caught by the LLM layer in §7.4.

### 7.4 LLM scoring (`internal/scoring`, applied to `stage='pending_score'`)

**This is where Coral Bricks earns its keep: many parallel calls sharing one large cached prefix.**

Request shape (`POST {CORAL_BASE_URL}/chat/completions`, `Authorization: Bearer $CORAL_API_KEY`):

```json
{
  "model": "glm-5.3-flash-fast",
  "prompt_cache_key": "wera-profile-<first 16 chars of profile sha256>",
  "temperature": 0.1,
  "max_tokens": 600,
  "messages": [
    {"role": "system", "content": "<SYSTEM PROMPT: fixed text below>"},
    {"role": "user",   "content": "CANDIDATE PROFILE:\n<profile.md verbatim>"},
    {"role": "user",   "content": "JOB:\nCompany: ...\nTitle: ...\nLocation: ...\nURL: ...\n\nDescription:\n<plain text, truncated to 12,000 chars>"}
  ]
}
```

**Prefix-caching rule:** the system prompt and the profile message must be **byte-identical on every call** and come **before** the job. Only the last message varies. This makes the long, shared part a free cached read after the first call.

**System prompt** (store in `internal/scoring/prompt.go`):

> You evaluate job postings for one candidate. Return ONLY a JSON object, no prose, no markdown fences, matching this schema exactly. Be strict and literal: base `sponsorship`, `years_required`, and `us_eligible` only on what the posting says. If the posting does not mention sponsorship, use "unknown". `sponsorship_quote` must be copied verbatim from the posting or be null. Score fit 0–100 for THIS candidate considering role type, seniority, required skills, and location. Entry/junior infrastructure, SRE, platform, DevOps, trading-operations, and ML-infrastructure roles that match the candidate's skills should score highest. Penalize roles requiring 4+ years, deep specialization the candidate lacks, or non-infrastructure work.

**Required JSON output:**

```json
{
  "fit_score": 0,
  "verdict": "strong|good|stretch|poor",
  "seniority": "entry|junior|mid|senior|unknown",
  "years_required": null,
  "sponsorship": "yes|no|unknown",
  "sponsorship_quote": null,
  "us_eligible": true,
  "work_mode": "remote|hybrid|onsite|unknown",
  "location_summary": "",
  "skills_matched": [],
  "skills_missing": [],
  "reason": ""
}
```

**Handling the response:**
- Strip accidental ```` ``` ```` fences, then `json.Unmarshal` into a struct and validate enums and ranges. On parse/validation failure, retry **once** with an extra user message: *"Your previous reply was not valid JSON for the schema. Return only the JSON object."* Second failure → `stage='score_failed'` with the raw text saved.
- Read `usage.prompt_tokens`, `usage.completion_tokens`, and `usage.prompt_tokens_details.cached_tokens` (**log the full `usage` object on the first call and adjust field names if Coral differs**). Compute `cost_usd` = (uncached input × input price + completion × output price) / 1e6, using the price table.
- **Post-LLM exclusion** (sets `stage='excluded'`, keeps the analysis row):
  - `sponsorship == "no"` and `sponsorship_quote` non-empty → `llm:sponsorship_no`
  - `years_required > max_years_required` → `llm:years>N`
  - `us_eligible == false` → `llm:non_us`
  - `seniority == "senior"` → `llm:senior`
- Otherwise `stage='scored'`.

**Concurrency and limits:**
- `SCORING_CONCURRENCY` (default 12) parallel requests via semaphore.
- 60s timeout per call. On 429: honor `Retry-After` if present, otherwise exponential backoff (1s, 2s, 4s, 8s, max 5 tries) and **halve the effective concurrency for the rest of the run**.
- Budget guard: env `MAX_COST_PER_RUN_USD` (default `0.50`). Stop scoring when reached; leftover jobs stay `pending_score` for the next run.

**Rescoring:** `wera rescore --all` re-queues scored jobs when `profile.md` changes (new `profile_hash`). Old analyses are kept.

### 7.5 Finish the run

Update the `runs` row with totals and status (`ok`, `partial` if any company failed, `failed` on a fatal error). Release the lock. Emit metrics.

---

## 8. Making Coral Bricks count (long-running work)

Two workloads go beyond one-off calls:

1. **Backfill batch.** The first run scores the whole backlog (likely hundreds of jobs) in parallel against one cached profile. `wera bench` prints the stats for the showcase:
   ```
   jobs scored: 214   wall time: 41.2s   throughput: 5.2 jobs/s
   prompt tokens: 712,330   cached: 498,210 (69.9%)   completion: 38,904
   cost: $0.051   cost/job: $0.00024
   ```
   It also runs the same 20 jobs with `SCORING_CONCURRENCY=1` and prints the speedup.

2. **Deep review (Milestone 7, optional).** For the top N scored jobs (`fit_score >= 75`), run a longer analysis with `CORAL_DEEP_MODEL` using **background mode** (`"background": true`, poll by response id). It produces:
   - a 3-bullet "why I fit" summary
   - the 3 resume bullets most worth emphasizing for this job
   - gaps and how to address them honestly
   - 3 likely interview topics

   Stored as `analyses.kind='deep'`. This is the long-running, agent-style workload Coral is built for.

---

## 9. REST API (`wera serve`)

All JSON. Default list filters hide `stage IN ('excluded','score_failed')` and `closed_at IS NOT NULL`.

| Method | Path | Purpose |
|---|---|---|
| GET | `/healthz` | `200 ok` if DB reachable |
| GET | `/metrics` | Prometheus |
| GET | `/api/jobs` | List. Query: `group`, `category`, `min_score`, `sponsorship` (yes/unknown), `work_mode`, `status`, `q` (title/company search), `since`, `posted_within` (days since the posting went up), `include_excluded`, `sort` (`score`/`newest`, newest by posting date), `limit`, `offset` |
| GET | `/api/jobs/{id}` | Job + latest `score` analysis + `deep` analysis if any + application |
| PUT | `/api/jobs/{id}/application` | Body `{status, notes}`. Sets `applied_at` when status first becomes `applied` |
| DELETE | `/api/jobs/{id}/application` | Takes the job off the tracker (no status, no notes): undoes a status set by mistake. Returns the job |
| GET | `/api/today` | Review queue: scored, open jobs with no application status yet (or only saved), sorted by `fit_score` desc |
| GET | `/api/stats` | Counts by stage/group/category/status, new jobs per day (14 days), applications per week |
| GET | `/api/runs?limit=20` | Recent runs |
| GET | `/api/companies` | Companies with last fetch status, job counts |
| GET | `/api/excluded?reason=` | Audit view of excluded jobs with evidence |
| * | `/api/admin/*` | The admin app (wera-admin): overview, users (edit, sign out, delete), runs (list, trigger: 409 if one is running), usage, companies, content moderation, email, audit log. Admin browser sessions only |

CORS: allow `http://localhost:5173` in dev (Vite). Bind to `localhost` by default; on the server, access through Tailscale.

---

## 10. Observability

- **Logs:** `slog`, one line per company fetch (`company`, `ats`, `jobs`, `ms`, `err`) and per scoring call (`job_id`, `model`, `tokens`, `cached`, `cost`, `ms`). **Never log the API key or full profile.**
- **Metrics:** `wera_fetch_total{company,result}`, `wera_fetch_duration_seconds`, `wera_jobs_new_total`, `wera_jobs_excluded_total{reason}`, `wera_llm_requests_total{model,result}`, `wera_llm_tokens_total{type}`, `wera_llm_cost_usd_total`, `wera_llm_latency_seconds`, `wera_run_last_success_timestamp`.
- These feed a Grafana dashboard later (pulse-platform style).

---

## 11. CLI

```
wera migrate                 # apply DB migrations
wera companies validate      # load YAML, report duplicates/bad ATS values
wera discover <name> [slug]  # try greenhouse/lever/ashby with candidate slugs, print which return jobs
wera fetch [--company X]     # fetch + normalize + rule filter only (no LLM)
wera score [--limit N]       # score pending jobs
wera pipeline                # one full run
wera worker                  # loop pipeline every RUN_INTERVAL
wera serve                   # REST API
wera bench [--n 200]         # scoring benchmark for the showcase
wera rescore --all           # requeue after profile change
wera refilter --all          # reapply roles.yaml rules to all open jobs (no LLM cost)
wera deep [--top N]          # Milestone 7
wera healthcheck              # exit 0 if DB reachable (container healthchecks)
```

`discover` derives candidate slugs from the name (`"Fireworks AI"` → `fireworksai`, `fireworks-ai`, `fireworks`) and probes all three ATS endpoints.

---

## 12. Local development (Ubuntu laptop)

```bash
cp .env.example .env              # add CORAL_API_KEY
cp profile/profile.example.md profile/profile.md   # paste resume + preferences
docker compose up -d postgres
make migrate                      # go run ./cmd/wera migrate
go run ./cmd/wera discover "Fireworks AI"
go run ./cmd/wera fetch           # no LLM spend
go run ./cmd/wera score --limit 5 # tiny paid test
go run ./cmd/wera pipeline
go run ./cmd/wera serve           # http://localhost:8080/api/today
```

`Makefile` targets: `build`, `test`, `lint` (`go vet`), `migrate`, `fetch`, `score`, `pipeline`, `serve`, `worker`, `bench`.

---

## 13. Deployment (Ubuntu server, after the frontend)

- `Dockerfile`: multi-stage, static Go binary on `gcr.io/distroless/static`.
- `docker-compose.yml` services: `postgres` (named volume), `wera-api` (`wera serve`), `wera-worker` (`wera worker`), later `wera-web`. All with `restart: unless-stopped` and healthchecks.
- Mount `config/` and `profile/` read-only; secrets via `.env` on the server only.
- Access via **Tailscale**; nothing exposed publicly.
- Nightly `pg_dump` to a local backup folder (cron).

Status: the `Dockerfile` (multi-stage, distroless/static, nonroot) and the
`wera-api` / `wera-worker` compose services (read-only `config/` + `profile/`
mounts, `wera healthcheck` healthchecks, `restart: unless-stopped`) are
implemented and `docker compose config` validates. Image build and the
server-side steps (Tailscale, pg_dump cron) happen at deployment time.

---

## 14. Milestones and acceptance checks

**M0: Skeleton**
- `go.mod`, layout from §4, `.gitignore`, `.env.example`, `profile.example.md`, Makefile, docker-compose with Postgres, migrations, `wera migrate`.
- ✅ `docker compose up -d postgres && make migrate` creates all tables. `go build ./...` and `go vet ./...` pass.

**M1: Greenhouse end to end (no LLM)**
- Config loading + validation, Greenhouse adapter with fixture test, normalize, upsert, closed-job detection, rule filter with table tests, `wera fetch`, `runs` rows.
- ✅ `make test` passes. `wera fetch` pulls DRW, Akuna, HRT, Optiver, IMC, Together AI, CoreWeave. A SQL check shows non-US, senior, intern, and explicit no-sponsorship jobs as `excluded` with readable evidence, and infra titles as `pending_score`. Running `wera fetch` twice creates **no duplicates**.

**M2: Lever + Ashby + discover**
- Both adapters with fixture tests, `wera discover`, `wera companies validate`.
- ✅ Baseten, Modal, Lambda (Ashby) and Belvedere (Lever) fetch successfully. `discover` finds or rules out each `enabled: false` company; update YAML accordingly.

**M3: Coral scoring**
- Client, prompt, schema validation, retry, usage + cost accounting, post-LLM exclusions, concurrency + 429 backoff, budget guard, `wera score`.
- ✅ `wera score --limit 5` stores 5 analyses with tokens and cost; logs show `cached_tokens > 0` from the second call onward. A job containing "we do not sponsor visas" ends `excluded` with the quote. Unit tests cover JSON parsing, fence stripping, enum validation, and cost math using an `httptest` fake server.

**M4: Pipeline + worker**
- `wera pipeline` with advisory lock and run totals, `wera worker` loop with graceful shutdown on SIGINT/SIGTERM.
- ✅ Two `wera pipeline` processes started together: second exits cleanly. `runs` shows correct totals and cost.

**M5: API + metrics**
- All endpoints in §9, `/healthz`, `/metrics`, CORS for Vite.
- ✅ `curl` checks for every endpoint return sensible JSON; `PUT` application status persists; `/metrics` exposes the counters.

**M6: Benchmark + backfill**
- `wera bench`.
- ✅ Prints throughput, cache %, cost per job, and the parallel-vs-serial speedup. Save the output for the showcase post.

**M7 (optional): Deep review with background mode.**
- ✅ `wera deep --top 3` queues three background Responses (glm-5.3-fast), polls them to completion in ~13s wall (server-side parallel), and stores kind='deep' rows at 97-99% prompt cache, $0.037 total. GET /api/jobs/{id} serves the deep analysis alongside the score.

---

## 15. Final end-to-end checklist (run before starting the frontend)

1. `git status` shows **no** `.env`, `profile/profile.md`, or binaries staged. `git grep cb_` finds nothing.
2. Fresh DB: `docker compose down -v && docker compose up -d postgres && make migrate`.
3. `make test` and `go vet ./...` pass.
4. `wera companies validate` reports no errors.
5. `wera pipeline` completes with status `ok` or `partial` (partial only for known-bad tokens).
6. SQL sanity checks:
   ```sql
   SELECT stage, count(*) FROM jobs GROUP BY 1;
   SELECT exclude_reason, count(*) FROM jobs WHERE stage='excluded' GROUP BY 1 ORDER BY 2 DESC;
   SELECT title, exclude_evidence FROM jobs WHERE exclude_reason LIKE '%sponsor%' LIMIT 10;   -- read these: all must be genuine refusals
   SELECT j.title, c.name, a.fit_score, a.sponsorship, a.years_required
     FROM jobs j JOIN companies c ON c.id=j.company_id JOIN analyses a ON a.job_id=j.id
    WHERE j.stage='scored' ORDER BY a.fit_score DESC LIMIT 20;                                -- read these: do the top jobs look right?
   SELECT sum(cost_usd), sum(cached_tokens)::float/nullif(sum(prompt_tokens),0) FROM analyses;
   ```
7. Spot-check 5 excluded and 5 scored jobs by opening their URLs. Adjust `roles.yaml` if anything is wrong, then rerun.
8. Run `wera pipeline` again immediately: **0 new jobs, 0 LLM calls**, no duplicates.
9. `wera serve`, then `curl localhost:8080/api/today` and `/api/stats` return data.
10. Leave `wera worker` running for 2 cycles; `runs` shows two new rows and no overlap.

When all ten pass, the backend is done.

**Acceptance (2026-10-06, branch `dev`):** all ten passed against a freshly
rebuilt database. Item 2 used the native PostgreSQL 18 install on :5433
(`DROP DATABASE wera WITH (FORCE)` + recreate + `make migrate`) instead of the
compose container; item 10 used `RUN_INTERVAL=1m` for a fast two-cycle check
(runs 3 and 4: status `ok`, 0 new jobs, $0, no overlap, graceful SIGTERM).
Fresh pipeline: 2,517 jobs, 2,404 rule-excluded, 113 scored (71 scored +
42 post-LLM excluded), $0.054 at 88.7% cache; spot-checked exclusions were
all genuine (verbatim sponsorship quotes, literal "5+ years" requirements);
`wera deep --top 3` re-verified on the fresh DB ($0.051, 97–99% cache). **The
backend is done.**

---

## 16. How to add things later (no code changes)

- **New company:** run `wera discover "Name"`, add the line to `companies.yaml`, run `wera companies validate`, then `wera pipeline`.
- **New role type:** add a category and patterns to `roles.yaml`, run `wera pipeline` (filters only run on new/changed jobs; use `wera refilter --all` to reapply to existing ones, which costs nothing).
- **New ATS:** the only case needing code: add a package implementing `Source`, register it, add fixture tests.
