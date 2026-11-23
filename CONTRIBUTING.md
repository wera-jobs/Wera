# Contributing to Wera

## Setup

Requirements: Go (see `go.mod`), Docker, and `golangci-lint`.

```sh
cp .env.example .env               # fill in AI_API_KEY; never commit .env
docker compose up -d postgres
export DATABASE_URL=postgres://wera:wera@localhost:5433/wera_dev?sslmode=disable
go run ./cmd/wera migrate
go run ./cmd/wera serve            # API on :8080
```

Always export `DATABASE_URL` yourself: the binary loads `.env`, whose URL may
point at the live database.

## Checks before a pull request

```sh
make lint                          # golangci-lint, zero findings
make test                          # needs WERA_TEST_DATABASE_URL (a copy, never "wera")
make cover                         # CI requires 70% of internal/...
make vuln                          # govulncheck
```

Integration tests share one database, so `scripts/test.sh` runs the
packages that use it one at a time and every other package in parallel
beside them. They create and remove their own fixtures.

## Layout

- `cmd/wera`: the single binary and its subcommands.
- `internal/`: one package per concern (`api`, `pipeline`, `scoring`,
  `store`, `sources`, ...). Database access lives in `internal/store` only.
- `migrations/`: goose migrations, additive whenever possible. Never edit
  one that has been applied; add a new one.

## Branches and pull requests

- Work on a branch from `dev` and open a pull request into `dev`. `dev` then
  reaches `main` through its own pull request. CI must pass at each step.
- Keep commits small and focused: one logical change each.
- Commit subjects follow [Conventional Commits](https://www.conventionalcommits.org):
  `type(scope): subject`, lowercase and imperative, no trailing period, e.g.
  `feat(tracker): drag jobs between columns` or `fix(api): refuse expired tokens`.
  Types: `feat`, `fix`, `perf`, `refactor`, `docs`, `test`, `build`, `ci`, `chore`.
  The body says why. A pull request check enforces the format.
- Releases are automatic: release-please reads the commits on `main` and keeps
  a release pull request with the next version and the changelog. Merging it
  cuts the release.

## Code style

- Write code that reads like the code around it: same naming, same comment
  density, same idioms.
- Comments explain why, not what. Exported names get a doc comment.
- Every lint exception names its rule and says why.
- New behavior comes with tests; bugs get a test that fails before the fix.

## Diagrams

The architecture diagrams are Excalidraw files; see `docs/diagrams/README.md`.
