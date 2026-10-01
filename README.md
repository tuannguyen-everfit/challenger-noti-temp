# go-service-template

Go microservice template extracted from challenger-service (platform/infra/stdx + conventions, no business features). Backed by MongoDB (replica set), Valkey, and Kafka (optional, opt-in via `SVC_KAFKA_BROKERS`).

> **Conventions:** Repo-wide rules (Go-mindset STOP list, file layout, Kafka topic/group naming,
> error mapping, …) live in [`CLAUDE.md`](./CLAUDE.md). This README is the operator entry point —
> install, run, deploy. Read CLAUDE.md before contributing code.

---

## Prerequisites

This repo uses **[mise](https://mise.jdx.dev/)** to pin tool versions. After installing mise once,
all other tools (Go, golangci-lint) install automatically.

```bash
# install mise (one-time, https://mise.jdx.dev/getting-started.html)
curl https://mise.run | sh

# from this repo, install every tool pinned in mise.toml
mise install

# verify
go version             # 1.25.x
golangci-lint --version
```

Also required:
- **Docker Desktop** (or Colima / OrbStack) — for `make compose-up`
- **make** — invoking targets

> If a tool listed in `mise.toml` is missing, `mise install` fixes it. Do not install Go or
> golangci-lint via Homebrew / apt — versions will drift from the team's pin.

---

## Quick start

```sh
mise install                                     # installs Go + golangci-lint + lefthook
cp mise.local.example.toml mise.local.toml       # local env (gitignored)
make compose-up                                  # mongo + valkey (waits for healthy)
go run ./cmd/server                              # mise auto-loads mise.local.toml
curl http://localhost:7991/readiness
```

For Kafka-backed features (cross-feature event flow):

```sh
make compose-up-kafka                            # mongo + valkey + zookeeper + 3 brokers + UI :8081
# add to mise.local.toml: SVC_KAFKA_BROKERS = "localhost:19092,localhost:29092,localhost:39092"
go run ./cmd/server
```

Env vars come from **`mise.local.toml`** (gitignored — copy from `mise.local.example.toml`).
mise auto-loads it whenever it's active in this directory, so `go run`, tests, and `make`
targets all inherit them — no shell `export` needed.

```sh
# Debug individual services (host ports differ from container ports to avoid collisions):
mongosh mongodb://localhost:27018
valkey-cli -p 6382 ping
```

---

## Architecture

HTTP entrypoint via chi, MongoDB for persistence (replica-set for transactions), Valkey for caching / idempotency replay, Kafka for cross-feature event flow (optional — features that need events opt in via the `EventPublisher` interface).

Observability is OpenTelemetry-only — no Prometheus middleware. `OTEL_EXPORTER_OTLP_ENDPOINT` set → full traces + metrics; unset → no-op pass-through.

See [`CLAUDE.md`](./CLAUDE.md) §3 for the directory layout and the conventions every new feature follows.

### Features

Add new features under `internal/features/<name>/` following the template in [.claude/rules/layout.md](.claude/rules/layout.md) §2. The template includes the notification feature (GET feed, summary); Bearer auth is always enabled. Add public features in `router.go` under the "Public feature mounts" comment; mount authed features in the BearerAuth group.

---

## Configuration

All variables use the `SVC_` prefix. For local development copy
`mise.local.example.toml` to `mise.local.toml` (gitignored); mise auto-loads it.

| Env var                         | Default      | Required |
|---------------------------------|--------------|----------|
| `SVC_HTTP_PORT`          | `7991`       | no       |
| `SVC_HTTP_TIMEOUT`       | `30s`        | no       |
| `SVC_LOG_LEVEL`          | `info`       | no       |
| `SVC_LOG_FORMAT`         | `json`       | no       |
| `APP_ENV` / `APP_NAME` / `APP_REGION` | `local` / `go-service-template` / `ap-southeast-1` | no |
| `SVC_MONGO_URI`          | —            | yes      |
| `SVC_MONGO_DATABASE`     | `service`    | no       |
| `SVC_MONGO_PING_TIMEOUT` | `5s`         | no       |
| `SVC_VALKEY_ADDR`        | —            | yes      |
| `SVC_VALKEY_PASSWORD`    | (empty)      | no       |
| `SVC_VALKEY_DB`          | `0`          | no       |
| `SVC_KAFKA_BROKERS`      | (empty)      | no — empty disables Kafka producer + all consumers |
| `SVC_SHUTDOWN_TIMEOUT`   | `15s`        | no       |
| `SVC_AUTH_JWT_ACCESS_SECRET` / `SVC_AUTH_JWT_REFRESH_SECRET` | — | yes — boot fails without both (every public route is Bearer-authed) |
| `SVC_PAGINATION_MAX_LIMIT` | `100` | no |
| `SVC_NOTIFICATION_INTERNAL_API_SECRET` | (empty) | no — empty leaves the account-deletion purge route unmounted; when set, ≥ 32 bytes |

`MONGO_URI` must contain `replicaSet=` — enforced at startup to guarantee dev/prod parity for transactions.

**OpenTelemetry env vars** follow the OTel spec (no `SVC_` prefix) — `OTEL_EXPORTER_OTLP_ENDPOINT`, `OTEL_SERVICE_NAME`, `OTEL_TRACES_SAMPLER_ARG`, etc. Unset → no-op SDK; set → full OTLP/gRPC export.

---

## Local development

The compose stack (`docker-compose.local.yml`) starts:

- `mongo:7` — single-node replica set `rs0` (unlocks transactions). Host port `27018`.
- `valkey/valkey:8.1-alpine` — host port `6382` (avoids collisions with other local redis containers).
- `app` — the service itself, with env wired to the compose-internal service names.

Two ways to run the app:

1. **`go run` against compose-started infra** (faster edit loop, no hot-reload). `mise.local.toml`
   points at host ports (`localhost:27018`, `localhost:6382`); mise loads it automatically.

   ```sh
   docker compose -f docker-compose.local.yml up -d mongo valkey
   go run ./cmd/server
   ```

2. **Everything in compose** (closer to prod). The `app` service uses compose-internal names
   (`mongo:27017`, `valkey:6379`) set inline in `docker-compose.local.yml` — `mise.local.toml`
   is not consulted for the containerized run.

   ```sh
   make compose-up       # build app image + start full stack
   make compose-logs     # tail logs
   make compose-down     # stop and remove volumes
   ```

---

## GitHub workflows — use `gh`

Prefer the GitHub CLI (`gh`) over the web UI, `curl`, or raw `https://api.github.com/...` for every GitHub operation against this repo. Same commands work locally, in CI, in Makefile targets, and in AI sessions — auth is shared (one `gh auth login`) and the output is pipe-friendly.

Install once: <https://cli.github.com/>. Then `gh auth login`.

```sh
gh pr view                       # view the PR for the current branch
gh pr list --state=open          # list open PRs
gh pr diff [N]                   # diff of PR #N
gh pr checks [N]                 # CI status for PR #N
gh pr create --title "..." --body "..."
gh pr comment [N] --body "..."
gh pr review [N] --approve
gh issue view [N] / gh issue list
gh api repos/Everfit-io/go-service-template/...   # raw API — instead of curl
```

Full rule + STOP triggers: [`.claude/rules/git.md`](.claude/rules/git.md) §5.

---

## Health endpoints

| Path           | Purpose                                              |
|----------------|------------------------------------------------------|
| `/healthcheck` | Dumb "process up" probe; no I/O                      |
| `/liveness`    | k8s liveness — returns 503 once shutdown begins      |
| `/readiness`   | k8s readiness — pings mongo + valkey, 503 if either down |

---

## Testing

```sh
make test                 # unit tests (race detector)
make test-integration     # build-tag `integration`; requires `make compose-up` first
make coverage             # report + fail if total < 45%
```

Service-surface integration tests (paused today — see `.claude/rules/testing.md` §1) live in `internal/features/<feature>/service_integration_test.go` (build-tag `integration`). They drive Service end-to-end against the real Mongo + Valkey (+ Kafka, when opted in) from `make compose-up`.

---

## Linting

```sh
make lint             # golangci-lint (v2 schema; .golangci.yml)
make fmt              # gofmt + goimports
make hooks            # one-time: install lefthook pre-commit + pre-push hooks
```

> **PATH note:** if you have golangci-lint installed elsewhere (e.g. nvim-mason's
> `~/.local/share/nvim/mason/bin`), it can shadow the mise pin. Verify with
> `which golangci-lint` — should resolve to a mise shim. Invoke as
> `mise exec -- golangci-lint run` to force the pinned version.

---

## Project layout

See [.claude/rules/layout.md](.claude/rules/layout.md) for the annotated directory tree, bucket-admission rules (`features/` / `infra/` / `platform/` / `stdx/`), and the in-file ordering convention.

---

## Convention docs

Topical rules live in [.claude/rules/](.claude/rules/). `CLAUDE.md` is the routing index — every change either matches a STOP trigger there or follows the linked rule.

| File | When to read |
|---|---|
| [mongo.md](.claude/rules/mongo.md) | `bson:` tags, queries, indexes, cursor pagination |
| [http.md](.claude/rules/http.md) | routes, DTOs, error envelope, validation |
| [headers.md](.claude/rules/headers.md) | response-header catalog (Retry-After, WWW-Authenticate, Allow, …) |
| [routing.md](.claude/rules/routing.md) | API versioning, edge throttle, pool tuning |
| [rfcs.md](.claude/rules/rfcs.md) | RFC conformance map + deliberate deviations |
| [cross-feature.md](.claude/rules/cross-feature.md) | one feature calling another (sync or Kafka) |
| [kafka.md](.claude/rules/kafka.md) | topic + group naming, JSONHandler, idempotency |
| [logging.md](.claude/rules/logging.md) | `log(ctx)` helper, module tagging, OTel-aligned fields |
| [testing.md](.claude/rules/testing.md) | unit vs integration, hand-written mocks, what to mock |
| [concurrency.md](.claude/rules/concurrency.md) | `safego`, errgroup, shutdown registry, leak prevention |
| [layout.md](.claude/rules/layout.md) | directory tree, file naming, DI, `stdx/` admission rules |
| [git.md](.claude/rules/git.md) | branch + commit format, `gh` CLI workflow |

---

## Versioning & release

The single source of truth is the **`internal/platform/buildinfo/VERSION`** file. Go has no
`package.json` equivalent — module versions live in git tags, not `go.mod`. We use a `VERSION`
file so the current release is visible at a glance and shows up in PR diffs.

```sh
make version          # prints internal/platform/buildinfo/VERSION contents (e.g. "0.1.0")
```

The file is **embedded into the binary** via `//go:embed` (`internal/platform/buildinfo/buildinfo.go`),
so plain `go run ./cmd/server` reports the same version as `make build`. Why the deep path?
`go:embed` directives can't reference files outside the package directory — keeping the file
next to the Go code that embeds it is the only working layout.

Resolution priority at runtime:

1. `-ldflags -X main.version=…` (set by Makefile from the VERSION file).
2. Embedded `internal/platform/buildinfo/VERSION` contents.
3. `"dev"` literal fallback.

`commit` follows: ldflags (`git rev-parse --short HEAD` + `-dirty`) → `runtime/debug.ReadBuildInfo`
VCS revision → `"unknown"`.

### Cutting a release

```sh
# 1. bump
echo "0.2.0" > internal/platform/buildinfo/VERSION

# 2. commit
git add internal/platform/buildinfo/VERSION
git commit -m "release: 0.2.0"

# 3. tag (matches the file)
git tag v0.2.0

# 4. push
git push origin HEAD --tags
```

CI / DevOps pipeline picks up the tag and builds an image tagged `:0.2.0` + `:latest`.

---

## Starting a new service from this template

1. Copy the folder, then rename: module path in `go.mod` + imports
   (`github.com/Everfit-io/go-service-template`), env prefix `SVC_` (config.go
   `SetEnvPrefix` + every `SVC_` string), `APP_NAME` / `OTEL_SERVICE_NAME`, and
   the Kafka topic prefix placeholder `xx-` in `.claude/rules/kafka.md`.
2. Reset `internal/platform/buildinfo/VERSION` if needed.
3. Add the first feature per `.claude/rules/layout.md` §2 (code + `docs/features/<name>/`),
   wire it in `internal/app/app.go` and mount it in `internal/platform/httpx/router.go`.
4. Run `scripts/smoke.sh` to confirm the bootstrap still works end to end.
