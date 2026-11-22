# Wera

[![CI](https://github.com/wera-jobs/Wera/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/wera-jobs/Wera/actions/workflows/ci.yml)
[![Coverage](https://img.shields.io/endpoint?url=https://raw.githubusercontent.com/wera-jobs/Wera/badges/coverage.json)](https://github.com/wera-jobs/Wera/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/wera-jobs/Wera?sort=semver&display_name=tag)](https://github.com/wera-jobs/Wera/releases)
[![Go](https://img.shields.io/github/go-mod/go-version/wera-jobs/Wera?logo=go)](go.mod)
[![Conventional Commits](https://img.shields.io/badge/Conventional%20Commits-1.0.0-fe5196?logo=conventionalcommits&logoColor=white)](https://www.conventionalcommits.org)
[![License](https://img.shields.io/github/license/wera-jobs/Wera)](LICENSE)

**Wera** (Swahili slang for a job or gig) is the backend for [wera-jobs.com](https://www.wera-jobs.com). It pulls postings from public job-board APIs for 5,100+ companies across 30 industries, filters and ranks them per user, scores the best against each resume with an OpenAI-compatible LLM, and serves the REST API. Wera never applies on your behalf.

Related repositories: [Wera-Frontend](https://github.com/wera-jobs/Wera-Frontend) (dashboard), [wera-extension](https://github.com/wera-jobs/wera-extension) (Chrome extension), [wera-admin](https://github.com/wera-jobs/wera-admin) (admin console).

## Architecture

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="docs/architecture-dark.svg">
  <img alt="Wera system architecture on one Hetzner VM: Caddy routes /api to wera-api and pages to wera-web; wera-api and wera-worker (one Go binary) use wera-embed and PostgreSQL and call the LLM API; the worker polls job boards, the API sends email; wera-admin is reached only through an SSH tunnel" src="docs/architecture.svg">
</picture>

One Go binary (`wera`) runs as both `wera-api` (HTTP API + email delivery) and `wera-worker` (scheduled pipeline). Both share PostgreSQL and `wera-embed` (a local CPU embedding service).

- **VPS ([deploy/hetzner](deploy/hetzner)):** Caddy terminates TLS and routes `/api/*` to `wera-api` and everything else to `wera-web`. `wera-admin` listens on `127.0.0.1` only via SSH tunnel.
- **Home server ([deploy/home](deploy/home)):** The public dashboard runs on Vercel and reaches `wera-api-public` over a Cloudflare Tunnel, while LAN devices use `wera-web` directly.

## Matching pipeline

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="docs/pipeline-dark.svg">
  <img alt="The matching funnel, to scale: 88k open jobs are fetched once for everyone, rules leave 2,900 per user, shared LLM facts and fact rules leave 1,800, a local model ranks them, and at most 100 a day get an LLM fit score at $0.0001 each" src="docs/pipeline.svg">
</picture>

*(The diagram uses an early snapshot; Wera now tracks 500,000+ open postings with the same funnel shape.)*

1. **Fetch:** Poll each company's public ATS feed once for all users (Greenhouse, Lever, Ashby, Workday, SmartRecruiters, Oracle, Workable, Rippling, Eightfold, Amazon) with ETags, shared throttling, and incremental Workday sweeps.
2. **Store:** Upsert new or changed postings by content hash. Postings only close after a successful board fetch.
3. **Rule filter:** Apply per-user preferences and `config/roles.yaml` (role families, seniority, US-only, explicit sponsorship refusals) at zero LLM cost.
4. **Local estimate:** Rank immediately with TF-IDF so new accounts see ordered matches within seconds.
5. **Shared facts:** Extract structured facts once per posting (seniority, years required, verbatim sponsorship quote, work mode, skills, digest) and reuse them across all users.
6. **Fact exclusions:** Drop postings that fail user constraints (years, sponsorship refusal, location, seniority) before any per-user scoring call.
7. **Local embedding rank:** Embed posting facts and user profiles locally via `wera-embed`, blending similarity, skill coverage, level/years distance, and a per-user model fit to past AI scores.
8. **Quick grades:** Batch-grade 20 jobs per LLM call from their facts (~$0.00004/job) to order the full queue accurately. See [docs/quick-grades.md](docs/quick-grades.md).
9. **Fit score:** Score the top `SCORES_PER_DAY` matches per user (default 100) with a compact job card and cached profile prefix (~$0.00018/job). Opening any estimated job scores it on demand.

## Features

- **Job matching:** Per-user filters, 5-part fit scores (0-100) with reasons, matched/missing skills, and verbatim sponsorship quotes.
- **Resumes:** Structured editor with Typst rendering (~50 ms) in four templates (Ivy, Parse, Signal, Ledger), LaTeX import/export, one-page fitting, deterministic review checks, and per-job tailoring that rewords bullets without inventing facts.
- **Cover letters:** Generated from your profile and resume for a specific job without repeating the resume.
- **Sponsorship & H-1B data:** Posting sponsorship signals alongside certified US Department of Labor LCA filings (counts, new hires, offered wages by title, city, and wage level).
- **Interview prep:** Role-based quizzes, email work simulations, and job-specific prep for tracked applications.
- **Community:** Posts, comments, and reactions screened by an AI moderator before publishing.
- **Chrome extension API:** Revocable per-browser tokens, application details, answer bank, job saving from any URL, AI form filling, and tailored resume attachment.
- **Admin API:** `/api/admin/*` routes for [wera-admin](https://github.com/wera-jobs/wera-admin) covering overview, users, runs, AI spend, traffic, companies, moderation, email, and audit logs.
- **Operations:** Prometheus `/metrics`, `/healthz` with build version, per-user and global monthly AI budgets, and circuit breaking on consecutive provider errors.

## Quick start

Requirements: Go 1.27+, Docker, an OpenAI-compatible LLM API key, and [Typst](https://github.com/typst/typst/releases) 0.14 on `PATH` (included in the Docker image).

```bash
cp .env.example .env            # set AI_PROVIDER and AI_API_KEY
docker compose up -d postgres
make migrate                    # apply SQL migrations
go run ./cmd/wera serve         # API on http://localhost:8080
```

Run `go run ./cmd/wera pipeline` for a full fetch/filter/score pass, or start `../wera-frontend` with `npm run dev`. To import a markdown profile from the shell instead of the web setup flow, see [profile/README.md](profile/README.md).

## Configuration

- **AI providers:** Set `AI_PROVIDER` (`coral`, `deepinfra`, or `openai` with `AI_BASE_URL`), `AI_API_KEY`, and optionally `AI_MODEL`. Known model prices live in `internal/scoring/prices.go`; custom models take `AI_PRICE_INPUT`, `AI_PRICE_CACHED_INPUT`, and `AI_PRICE_OUTPUT`.
- **Companies & roles:** `config/companies.yaml`, `config/industries.yaml`, and `config/roles.yaml` drive fetching and filtering without code changes.
- **H-1B data:** Download quarterly LCA Excel files from the [OFLC performance data page](https://www.dol.gov/agencies/eta/foreign-labor/performance), then run:
  ```bash
  wera h1b import LCA_Disclosure_Data_FY2025_Q4.xlsx LCA_Disclosure_Data_FY2026_Q3.xlsx
  wera h1b match
  wera h1b status
  ```
- **Email:** Off until `MAIL_PROVIDER=smtp`, `MAIL_FROM`, `PUBLIC_URL`, and `SMTP_*` are set (configure SPF, DKIM, and DMARC on the sending domain). Use `MAIL_PROVIDER=log` in development and `wera mail preview --out email-preview` to inspect templates.

## Deployment & backups

- **VPS:** See [deploy/hetzner/README.md](deploy/hetzner/README.md) (GHCR images + Caddy auto-HTTPS).
- **Home server:** See [deploy/home/README.md](deploy/home/README.md) (LAN + Cloudflare Tunnel + `deploy/home/backup.sh`).
- **Docker Compose (local build):**
  ```bash
  WERA_VERSION=$(git describe --tags --always) WERA_COMMIT=$(git rev-parse --short HEAD) \
  WERA_WEB_COMMIT=$(git -C ../wera-frontend rev-parse --short HEAD) \
  WERA_ADMIN_COMMIT=$(git -C ../wera-admin rev-parse --short HEAD) \
  docker compose up -d --build
  ```
  Postgres (`5433`) and the API (`8080`) bind to `127.0.0.1` only. `wera-web` runs on `:3000` and `wera-admin` on `:3001` (private networks only). Use `--profile public` to add `wera-api-public` and `wera-tunnel`.
- **Manual backup & restore:**
  ```bash
  # Backup
  docker exec wera-postgres pg_dump -U wera -d wera | gzip > ~/wera-backups/wera-$(date +%Y%m%d-%H%M).sql.gz
  # Restore into a fresh DB, then swap while API/worker are stopped
  docker exec wera-postgres createdb -U wera wera_restored
  gunzip -c <file> | docker exec -i wera-postgres psql -q -U wera -d wera_restored
  ```

### Troubleshooting

| Symptom | Likely cause | Fix |
|---|---|---|
| Public site unreachable, LAN works | Cloudflare Tunnel dropped | Wait for auto-reconnect or run `docker restart wera-tunnel` |
| Run status is `partial` | One or more boards failed | Check the admin Companies page; nothing closes on failed fetches |
| Jobs stay "estimated" | Daily score cap or monthly budget hit | Open a job to score on demand, or adjust `SCORES_PER_DAY` / `wera users budget` |
| Integration tests skipped | `WERA_TEST_DATABASE_URL` unset | Point it at a non-live DB (e.g., `wera_test`); tests refuse a DB named `wera` |

## Development

```bash
make build      # bin/wera with stamped version
make lint       # golangci-lint
make test       # unit + integration tests (requires WERA_TEST_DATABASE_URL)
make cover      # coverage for internal/...
make vuln       # govulncheck
```

Security details and vulnerability reporting live in [SECURITY.md](SECURITY.md). Commits follow [Conventional Commits](https://www.conventionalcommits.org); [release-please](https://github.com/googleapis/release-please) cuts semantic versions and publishes `ghcr.io/wera-jobs/wera`.

## CLI commands

| Command | Purpose |
|---|---|
| `wera migrate` | Apply database migrations |
| `wera serve` | Run REST API, `/healthz`, and `/metrics` |
| `wera worker` | Run the pipeline on `RUN_SCHEDULE` / `RUN_INTERVAL` ([docs/run-schedule.md](docs/run-schedule.md)) |
| `wera pipeline` / `fetch` / `score` | Run full pipeline, fetch only, or score only |
| `wera discover "Name"` | Probe Greenhouse, Lever, and Ashby for a company's board |
| `wera companies validate` | Validate `config/companies.yaml` |
| `wera refilter --all` / `rescore --all` | Reapply rules (free) or requeue scoring after profile changes |
| `wera deep --top N` / `bench` | Deep review of top matches / scoring throughput benchmark |
| `wera users ...` | Manage accounts, admin role, budgets, and profile import/export |
| `wera mail ...` | Preview, test, queue digests, or flush queued mail |
| `wera h1b import\|match\|status` | Load DOL H-1B LCA files and match employers to companies |
| `wera version` | Print stamped version and commit |

## Project layout

```
cmd/wera              CLI entrypoint
internal/api          REST handlers (chi), security headers, rate limits
internal/auth         Password hashing, sessions, rate limiting
internal/config       Env, companies, industries, and role catalog
internal/filter       Rule engine driven by config/roles.yaml
internal/h1b          DOL LCA streaming importer, wage normalization, employer matching
internal/interview    Role quizzes, work simulations, job-specific prep
internal/logos        Company logo fetcher and cache
internal/mail         HTML/text templates, MIME, SMTP/log providers
internal/moderation   Community rule checks and AI review
internal/normalize    HTML-to-text and content hashing
internal/notify       Account emails, digests, outbox delivery
internal/pay          Posted pay extraction and H-1B wage comparison
internal/pipeline     Fetch, filter, rank, and score orchestration
internal/profile      Resume text, avatars, profile drafts, cover letters
internal/relevance    Local TF-IDF ranking
internal/resume       Typst rendering, one-page fit, LaTeX import/export, tailoring
internal/scoring      OpenAI-compatible LLM client, facts, fit, quick grades, cost tracking
internal/sources      ATS adapters (Greenhouse, Lever, Ashby, Workday, SmartRecruiters, Oracle, Workable, Rippling, Eightfold, Amazon)
internal/store        PostgreSQL repository (pgx)
migrations/           Embedded SQL migrations (goose)
config/               companies.yaml, industries.yaml, roles.yaml
deploy/               Hetzner VPS and home-server configs
docs/                 Diagrams and design notes
```

## Stack

Go 1.27, chi, pgx, PostgreSQL 16, goose, Typst, Snowflake Arctic Embed M v1.5 (TEI), Prometheus, Docker Compose, GitHub Actions, release-please.

See [PLAN.md](PLAN.md) for the original design and [wera-jobs/.github](https://github.com/wera-jobs/.github) for shared community files.
