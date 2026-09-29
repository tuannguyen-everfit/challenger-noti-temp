# CLAUDE.md — `go-service-template`

Repo-specific routing index for AI assistants and contributors. **Auto-loaded every session — kept lean.** Detail lives in [.claude/rules/](.claude/rules/).

---

## 0. Core principle

This is a **Go** service. Treat every change as Go from the first line — not "Node.js / Java / Python with Go syntax." Many habits from those languages produce *legal* Go code that fights the ecosystem (gofmt, gopls, golangci-lint, the stdlib) for the project's lifetime.

> ⚠️ **STOP rule.** If a code change matches a STOP trigger in §1, do not write the code. Instead output:
>
>     STOP: <pattern detected> — <one-sentence reason it's not Go-idiomatic>
>
> Then wait for the user to (a) override explicitly, or (b) confirm an alternative. Do not "compromise" silently.

> 📣 **Shared policy goes in team-visible files.** Conventions, STOP rules, decisions, and reversal steps belong in `CLAUDE.md` or `.claude/rules/*.md` — both are committed to the repo and load for every contributor's AI session. Per-developer memory (`~/.claude/projects/.../memory/`) does NOT ship with the repo and is invisible to teammates — reserve it for personal-session context only, never for project policy.

---

## 1. STOP triggers — quick-scan

Each trigger links to a [.claude/rules/](.claude/rules/) detail file. Match a trigger → read the linked doc → don't write the bad pattern.

### Mongo
- camelCase `bson` tags → use `bson:"snake_case"` ([mongo.md](.claude/rules/mongo.md))
- Offset/page pagination (`?page=`, `LIMIT n OFFSET m`, `total`/`pageCount`) → cursor only ([mongo.md](.claude/rules/mongo.md))
- New query without a matching `IndexSpec` in `indexes.go` → add or reuse a spec first ([mongo.md](.claude/rules/mongo.md))
- `BaseRepository[T]` generic embed → use `mongox.FindByID[T]` functions ([mongo.md](.claude/rules/mongo.md))
- Repo interface exposing `bson.M` to Service → typed params + domain models ([mongo.md](.claude/rules/mongo.md))
- Two features writing to the same Mongo collection → each feature owns its collection ([mongo.md](.claude/rules/mongo.md))
- Upsert on a struct with `bson:"_id"` / `bson:"created_at"` (no `omitempty`) → zero values marshal literally; use `omitempty` + `$setOnInsert` ([mongo.md](.claude/rules/mongo.md) §6)
- `delete(setDoc, "_id")` / `delete(setDoc, "created_at")` after marshal → fix the struct tag instead ([mongo.md](.claude/rules/mongo.md) §6)
- Same timestamp field in both `$set` and `$setOnInsert` → Mongo rejects with a path-conflict error ([mongo.md](.claude/rules/mongo.md) §6)

### HTTP / errors
- Headers with `X-` prefix (RFC 6648) → bare names (`Cache-Status`, `If-Match`) ([http.md](.claude/rules/http.md))
- Manual `io.ReadAll(r.Body)` + `json.Unmarshal` → use `typed.JSON` adapter ([http.md](.claude/rules/http.md))
- Hand-rolled `func (r *Req) Validate() error` → `validator/v10` struct tags ([http.md](.claude/rules/http.md))
- Domain struct with `json:` tags → wire DTO seam only (`toItemResponse`) ([http.md](.claude/rules/http.md))
- Class hierarchies of errors → one `apperr.AppError`, constructor functions ([http.md](.claude/rules/http.md))
- Returning raw internal error strings to clients → `apperr.*` or `respond.MapError` ([http.md](.claude/rules/http.md))
- Legacy Node envelope `{message, error, errorCode, additional, title}` → `{code, message, details?}` ([http.md](.claude/rules/http.md))
- camelCase JSON field tags (`expiresAt`, `nextCursor`) → `snake_case` for wire fields, paired with bson ([http.md](.claude/rules/http.md))
- Hardcoded per-environment ceiling on `?limit=` / `?window=` / bulk size via `validate:"max=N"` → ENV-tunable platform-or-feature `Config` + Service-layer reject ([http.md](.claude/rules/http.md) §11)
- `max=N` on BOTH validator tag AND Service check for the same field → one source of truth, Service-layer cap wins ([http.md](.claude/rules/http.md) §11)
- New `SVC_<FEATURE>_MAX_LIMIT` for `?limit=` → use the shared `SVC_PAGINATION_MAX_LIMIT` instead; feature-namespaced envs only for knobs whose semantics don't transfer to other features ([http.md](.claude/rules/http.md) §11)
- Adding a response header inline via `w.Header().Set(...)` from a handler error path → use `apperr.AppError.WithHeader` / `WithHeaderAdd` ([headers.md](.claude/rules/headers.md))
- `WWW-Authenticate` on a 403 response → 401 only; 403 = "auth fine, you're still not allowed" ([headers.md](.claude/rules/headers.md) §5)
- Returning 405 without `Allow` from a handler → `apperr.MethodNotAllowed(code, msg, "GET", "POST")` populates it (RFC 9110 §15.5.6 — [rfcs.md](.claude/rules/rfcs.md) §1)
- Returning 409 Conflict for optimistic-lock failure → use `apperr.PreconditionFailed` (412) ([rfcs.md](.claude/rules/rfcs.md) §1, RFC 9110 §15.5.13)
- Returning 400 when `If-Match` missing on a mutating endpoint that requires it → use `apperr.PreconditionRequired` (428) ([rfcs.md](.claude/rules/rfcs.md) §1, RFC 6585 §3)
- Success response on a version-bearing resource without `ETag` → implement `respond.HeaderProvider` (returns `ETag: W/"v<N>"`) ([rfcs.md](.claude/rules/rfcs.md) §1, RFC 7232)
- Reading client IP from `r.RemoteAddr` directly → `middleware.ClientIP` parses Forwarded / X-Forwarded-For ([rfcs.md](.claude/rules/rfcs.md) §1, RFC 7239)
- Reviewer cites "RFC X compliance" without first checking [rfcs.md](.claude/rules/rfcs.md) §2 → some deviations (RFC 9457 Problem Details, JSON Patch / Merge Patch) are deliberate

### Cross-feature
- Service A calling Repo of feature B → call B's Service via consumer-side interface ([cross-feature.md](.claude/rules/cross-feature.md))
- Feature struct holding `*featureB.Service` concretely → declare a narrow `featureBClient` interface ([cross-feature.md](.claude/rules/cross-feature.md))
- Handler reaching into another feature's Service → handler calls own Service, which calls the interface ([cross-feature.md](.claude/rules/cross-feature.md))
- Producer importing consumer (cycle risk) → invert dependency or extract to a third package ([cross-feature.md](.claude/rules/cross-feature.md))
- Same-package consumer with one-method dependency → pass the function value, not an interface ([cross-feature.md](.claude/rules/cross-feature.md))

### Kafka
- Topic name without `xx-` prefix or wrong shape → `xx-<source-feature>-<verb-past-tense>` ([kafka.md](.claude/rules/kafka.md))
- Consumer group missing event-name suffix → `xx-<subscriber>-<event-name>` ([kafka.md](.claude/rules/kafka.md))
- Reimplementing decode + commit-on-malformed → use `kafka.JSONHandler[T]` ([kafka.md](.claude/rules/kafka.md))

### Logging
- `slog.Default()` inside feature code → use the package's `log(ctx)` helper ([logging.md](.claude/rules/logging.md))
- Bare `logging.FromContext(ctx)` inside feature code → wrap via `log(ctx)` so `module=<feature>` is attached ([logging.md](.claude/rules/logging.md))
- Hand-rolled logging of request/response bodies, headers, auth tokens → don't (PII/secret risk). Opt-in capture via `SVC_HTTP_LOG_*` is the only sanctioned path ([logging.md](.claude/rules/logging.md) §6)

### DI / layout
- `wire`, `fx`, `dig`, decorator DI → manual DI in `internal/app/app.go` ([layout.md](.claude/rules/layout.md))
- `init()` setting a package-level mutable singleton → build in `main`, pass it in ([layout.md](.claude/rules/layout.md))
- Package-level mutable `var Service = service.New()` → construct in `app.go` ([layout.md](.claude/rules/layout.md))
- New `helpers/` `utils/` `common/` `generic/` package → find a real domain name ([layout.md](.claude/rules/layout.md))
- Adapter file like `repository_cache.go` (role-suffix) → `repository_valkey.go` (infra-suffix) ([layout.md](.claude/rules/layout.md))
- Bundling external systems behind a fat `Storage` interface → one interface per concern (`Repo`, `Cache`, `Outbox`, …) ([layout.md](.claude/rules/layout.md))
- New `stdx/<x>` with non-stdlib imports → belongs in `platform/` ([layout.md](.claude/rules/layout.md))
- New `stdx/<x>` for a single use site → inline first; promote when 2nd use lands ([layout.md](.claude/rules/layout.md))
- `stdx/utils` / `stdx/helpers` / `stdx/common` → bucket folders inside stdx are the same smell as elsewhere; name the concern ([layout.md](.claude/rules/layout.md))
- Splitting any production file before it **exceeds 1000 LoC** → LoC is the only trigger, not number of endpoints / methods / "flows" ([layout.md](.claude/rules/layout.md) §2.6)
- Splitting `service.go` while under 1000 LoC → service stays one file regardless of method count; splits LAST when splits happen ([layout.md](.claude/rules/layout.md) §2.6)
- Splitting `service_test.go` or `handler_test.go` — ever → tests don't split. If a test file feels huge, split the production file first ([layout.md](.claude/rules/layout.md) §2.6)
- `handler.go` over 1000 LoC → now split — group methods + DTOs by whatever reads naturally; keep `Routes()` + struct + consumer-side interface in `handler.go` ([layout.md](.claude/rules/layout.md) §2.6)

### Docs layout
- New `internal/features/<name>/` without a matching `docs/features/<name>/` directory → scaffolding is incomplete ([layout.md](.claude/rules/layout.md) §2.5)
- `docs/features/<name>/` missing one of `specs.md` / `test-cases.md` / `solution-design.md` / `implementation-plan.md` → all four are the minimum ([docs/README.md](docs/README.md))
- Feature-specific design notes filed under `docs/architectures/` → `docs/architectures/` is for cross-cutting design only; per-feature work goes in `docs/features/<name>/` ([layout.md](.claude/rules/layout.md) §2.5)
- Creating `docs/architecture/` (singular) → directory is `docs/architectures/` (plural) ([docs/README.md](docs/README.md))

### Concurrency
- Unwrapped `go func() { ... }()` → `safego.Go` or `safego.WrapErr` ([concurrency.md](.claude/rules/concurrency.md))
- `sync.Mutex.TryLock` for liveness state → `atomic.Bool` ([concurrency.md](.claude/rules/concurrency.md))
- `chan error` for one-error-out-of-many → `errgroup.WithContext` ([concurrency.md](.claude/rules/concurrency.md))
- `time.Sleep(N * time.Second)` in tests → `require.Eventually` / channels ([concurrency.md](.claude/rules/concurrency.md))
- New `safego.Go` / gRPC conn / external client in `app.go` without a row in `shutdown(...)` → register it ([concurrency.md](.claude/rules/concurrency.md) §5)
- HTTP drain using full `shutCtx` instead of the 80% sub-budget → OTel/Mongo tail needs reserved time ([concurrency.md](.claude/rules/concurrency.md) §5)
- Goroutine spawned inside an HTTP / gRPC handler → leak by definition; publish a Kafka event or use a registered worker ([concurrency.md](.claude/rules/concurrency.md) §6)
- `go` / `safego.Go` without a named owner that calls cancel → reject the diff ([concurrency.md](.claude/rules/concurrency.md) §6 reviewer checklist)
- Long-lived goroutine using `context.Background()` instead of a cancellable parent → leak ([concurrency.md](.claude/rules/concurrency.md) §6)
- `time.NewTicker` / `time.NewTimer` without `defer .Stop()` → runtime goroutine leak ([concurrency.md](.claude/rules/concurrency.md) §6)
- Test spawning a goroutine without `goleak.VerifyTestMain` or `goleak.VerifyNone` → leak slips past CI ([concurrency.md](.claude/rules/concurrency.md) §6)

### Testing
- Writing a new `integration_test.go` today → integration tests are SKIPPED ([testing.md](.claude/rules/testing.md) §1 banner)
- `service_integration_test.go` that actually covers all surfaces → either rename to `integration_test.go` or split per surface ([testing.md](.claude/rules/testing.md))
- `xxx_unit_test.go` filename → drop the `_unit_` qualifier; plain `_test.go` IS the unit test ([testing.md](.claude/rules/testing.md))
- Separate `tests/` directory → tests live next to the code in the same package ([testing.md](.claude/rules/testing.md))
- Handler test mocking Repo / Cache (not Service) → mock the IMMEDIATE consumer-side interface only ([testing.md](.claude/rules/testing.md) §6)
- Mocking `stdx/*`, `logging`, `apperr`, `pagination` → pure-Go, no I/O; use them as-is, inject determinism at Service boundary instead ([testing.md](.claude/rules/testing.md) §6)
- Fixture setup ignoring errors with `_, _ = svc.Op(...)` → `t.Helper()` seed function that `t.Fatalf`s on error ([testing.md](.claude/rules/testing.md) §7.1)
- Counter / spy field in a mock that no test asserts on → assert or delete; an unused counter is a latent missed assertion ([testing.md](.claude/rules/testing.md) §7.2)
- Naming a hand-written test double `fakeX` / `stubX` / `spyX` → project convention is `mockX` for any hand-written double ([testing.md](.claude/rules/testing.md) §4 terminology note)
- Mock receiver named `r` / `c` / `s` / `p` / `f` → every `mock*` uses receiver `m` (role-letters collide; `m` is uniform) ([testing.md](.claude/rules/testing.md) §4 receiver note)
- Test spawns a goroutine but the package has no `main_test.go` with `goleak.VerifyTestMain` → add the per-package `main_test.go` ([testing.md](.claude/rules/testing.md) §8)
- Clever code in tests (`strings.Repeat("0", 0)`, `string(rune('0'+i))`) → use `fmt.Sprintf` or stdlib helpers; tests are documentation ([testing.md](.claude/rules/testing.md) §7.3)
- `t.Errorf` on an assertion whose result is dereferenced by the next line → `t.Fatalf` ([testing.md](.claude/rules/testing.md) §7.4)
- Open-coding `svc := New(repo, cache, nil); svc.now = ...` in 2+ tests for an alternate constructor mode → add a `newSvc<Variant>` helper ([testing.md](.claude/rules/testing.md) §7.5)

### Git / commits / branches
- `curl https://api.github.com/...` or pasting a PR URL when you want an action → use `gh` CLI ([git.md](.claude/rules/git.md))
- Manual browser-click GitHub workflows when scriptable → `gh pr` / `gh issue` / `gh api` ([git.md](.claude/rules/git.md))
- Commit message without `[card]` after the tag → format is `[tag]([module]): UP-XXXXX [description]` ([git.md](.claude/rules/git.md))
- Commit message without `[tag]:` prefix → mandatory; feeds release-notes tooling ([git.md](.claude/rules/git.md))
- Card in `[brackets]` at end of description → strict format puts card right after `:` (`feat(items): UP-XXXXX add feature`) ([git.md](.claude/rules/git.md))
- Branch name not matching `[env]_[sprint].[tag]/[card]` → use `dev_s48.feat/UP-14545` shape ([git.md](.claude/rules/git.md))
- Auto-commit / auto-push without explicit user approval → never. User reviews staged diff first ([git.md](.claude/rules/git.md))
- `git commit --no-verify` → never; fix the failing hook instead ([git.md](.claude/rules/git.md))

### Routing / throttle / API versioning
- Feature `Routes()` hardcoding `/api/v1/...` paths → prefix is owned by `internal/platform/httpx/router.go` ([routing.md](.claude/rules/routing.md) §1)
- New unversioned public endpoint (anything but health probes) → mount under the versioned API group ([routing.md](.claude/rules/routing.md) §1)
- Wrapping health endpoints in `middleware.Throttle` → kubelet sees 503 under load and kills the pod ([routing.md](.claude/rules/routing.md) §2)
- `HTTPThrottleMax > min(downstream pool sizes)` → moves the bottleneck to where it surfaces as latency instead of fast 503 ([routing.md](.claude/rules/routing.md) §2)
- Adding `MaxPoolSize` / `PoolSize` config without metric/load-test/incident trigger → premature tuning ([routing.md](.claude/rules/routing.md) §3)

### Naming (functions, variables, constants)
- New helper prefix outside the §2 table (`assemble*`, `prepare*`, `process*`, `handle*`, `do*`, `gather*`) → use `build*` for output, `collect*` for gathering, `pick*` for subset selection ([naming.md](.claude/rules/naming.md) §2)
- Infra terms in Service helpers (`hasZSetData`, `filterMembers`, `parseMongoDoc` outside `repository_mongo.go`) → translate at the layer boundary; Service speaks domain ([naming.md](.claude/rules/naming.md) §4)
- Generic suffixes (`*Partial`, `*Helper`, `*Util`, `*Data`, `*Manager`) on types or files → find the real concept ([naming.md](.claude/rules/naming.md) §5)
- Cap / count constants named with implementation hints (`*Out`, `*Oversample`, `*Tmp`, `*Buffer`) → say what they MEAN: `randomPeersFetchSize`, not `randomPeersOversample` ([naming.md](.claude/rules/naming.md) §6)
- Exporting a constant just so a test can read it → tests sit in same package; keep unexported ([naming.md](.claude/rules/naming.md) §6)
- `*Request` / `*Response` on a Service-layer DTO → reserved for wire DTOs in `handler.go`. Service uses `*Input` / `*Result` ([naming.md](.claude/rules/naming.md) §1)
- `validate*` returning data, or `parse*` doing guards-only → split: `validate*` returns error, `build*` returns data ([naming.md](.claude/rules/naming.md) STOP rules)

### Clean code (inside-the-function judgment)
- Extracting a single-call helper just to shorten a parent function → length alone isn't the trigger; mixing phases is ([clean-code.md](.claude/rules/clean-code.md) §1)
- Control-flow nesting past 3 levels → flatten with guard clauses / extracted helper / `switch` ([clean-code.md](.claude/rules/clean-code.md) §2)
- Bare magic number or status string with non-obvious meaning → name it as a constant (`mnd` is NOT linted) ([clean-code.md](.claude/rules/clean-code.md) §3)
- A function/type doing two things (`validateAndBuild*`, Service method that also formats wire output) → single responsibility ([clean-code.md](.claude/rules/clean-code.md) §4)
- `errors.New("...")` dropping the cause, or `_ =` swallowing a fallible call uncommented → wrap with `%w` / handle ([clean-code.md](.claude/rules/clean-code.md) §5)
- Boolean flag parameter selecting between two behaviors → split the function ([clean-code.md](.claude/rules/clean-code.md) §6)
- "For testing" interface/abstraction with one production impl before a 2nd caller → YAGNI; inline ([clean-code.md](.claude/rules/clean-code.md) §6)

### Tooling / style
- `python -c "import json; ..."` in scripts/CI → use `jq` (house rule)
- `npm`, `yarn`, `node_modules` artifacts in scaffolding → not a Node project
- Dotenv loader via `_.file = ".env.local"` in `mise.toml` → use mise's native `mise.local.toml` + `mise.local.example.toml`
- Spaces for `.go` indentation → gofmt enforces tabs (set editor `tabSize`)
- `// File: foo.go` header banners, ASCII boxes, multi-paragraph file headers → one-line `// Package x ...` only
- Section dividers inside structs (`// === Public methods ===`) → godoc ordering + exported-first
- **Comments must be concise and non-redundant** — default to no comments; when one is needed, keep it short and only explain non-obvious WHY (a constraint, invariant, or workaround), never restate WHAT the code does ([layout.md](.claude/rules/layout.md) §2.4)
- Setters/getters wrapping plain fields → export the field
- `if err != nil { return nil, errors.New("failed") }` losing the cause → wrap with `%w`

---

## 2. When working on X, read its convention FIRST

| If your task touches… | Read | What's in it |
|---|---|---|
| `bson:` tags, `r.coll.*`, indexes, pagination | [.claude/rules/mongo.md](.claude/rules/mongo.md) | snake_case bson, cursor pagination contract, indexes-as-data, Repo/Service split |
| Routes, DTOs, `apperr`, `localization`, `respond`, error mapping | [.claude/rules/http.md](.claude/rules/http.md) | typed.JSON, error envelope, DTO seam, validator tags, headers |
| Adding a `?limit=` / `?window=` / bulk-size ceiling — anything ops should tune via env | [.claude/rules/http.md §11](.claude/rules/http.md) | Shared `PaginationConfig.MaxLimit` (default) vs per-feature `<Feature>Config` (exception), mapped to feature's local `Config`, Service-layer reject |
| Attaching response headers (Retry-After, WWW-Authenticate, Allow, Link, …) | [.claude/rules/headers.md](.claude/rules/headers.md) | per-header catalog, when each one matters, "do not send" list |
| Reviewer cites RFC compliance; deciding if a deviation is OK | [.claude/rules/rfcs.md](.claude/rules/rfcs.md) | RFCs honored, deliberate deviations (e.g. not RFC 9457), what's not yet relevant |
| Calling another feature, defining `featureClient` interface | [.claude/rules/cross-feature.md](.claude/rules/cross-feature.md) | interface-first, narrow subset, composition-root adapter |
| `internal/infra/kafka`, `consumer_kafka.go`, `Publish(...)` | [.claude/rules/kafka.md](.claude/rules/kafka.md) | topic/group naming, JSONHandler, idempotency |
| Adding logs anywhere in feature code | [.claude/rules/logging.md](.claude/rules/logging.md) | `log(ctx)` helper, module tagging, levels |
| New `_test.go` or `integration_test.go`; hand-written mocks | [.claude/rules/testing.md](.claude/rules/testing.md) | filename truth, build tag, clock injection, what to mock vs use as-is |
| `go func`, mutex, channel, errgroup; background workers | [.claude/rules/concurrency.md](.claude/rules/concurrency.md) | safego, atomic.Bool, errgroup pattern |
| File names, struct/file ordering, DI graph, new package layout, when to use / create `stdx/` | [.claude/rules/layout.md](.claude/rules/layout.md) | tree, in-file ordering, multi-backend rule, `repository_<infra>.go`, stdx usage + admission criteria |
| New branch, commit message, PR title | [.claude/rules/git.md](.claude/rules/git.md) | branch `[env]_[sprint].[tag]/[card]`, commit `[tag]([module]): [card] [description]` |
| New endpoint, throttle config, API version bump, considering pool tuning | [.claude/rules/routing.md](.claude/rules/routing.md) | versioned API group, edge throttle, pool-tuning criteria |
| Naming a new helper / variable / constant / type — choosing the prefix, avoiding generic suffixes, infra-vs-domain layering | [.claude/rules/naming.md](.claude/rules/naming.md) | verb-prefix table (`validate*`, `build*`, `compute*`, `collect*`, `pick*`, …), layer discipline, generic-suffix STOP list, constant placement |
| Writing/reviewing a function body — length, nesting, magic numbers, single responsibility, error clarity, code smells | [.claude/rules/clean-code.md](.claude/rules/clean-code.md) | the judgment-call half (what `gocyclo`/`errorlint`/revive DON'T catch); thresholds anchored to `.golangci.yml` |

---

## 3. Build, lint, test — what to run

| Task | Command |
|---|---|
| Run server locally | `make run` — hot reload via [air](https://github.com/air-verse/air), rebuilds on `.go` change (config in [.air.toml](.air.toml); mise auto-loads `mise.local.toml`). One-shot without watch: `go run ./cmd/server`. |
| Build binary | `make build` → `bin/go-service-template` with `-ldflags` version stamped |
| Unit tests | `make test` (race detector ON by default) |
| Integration tests | `make compose-up && make test-integration` |
| End-to-end smoke | `make smoke` — throwaway Mongo + Valkey containers (own ports), integration gate, real binary, HTTP probes, SIGTERM shutdown check |
| Lint | `make lint` (must report `0 issues`) |
| Format | `make fmt` (gofmt + goimports with local-prefix grouping) |
| Coverage | `make coverage` (gate ≥ 45%; CI enforces) |
| Bring up compose | `make compose-up` (mongo + valkey + app) / `make compose-up-kafka` / `make compose-up-signoz` (full observability stack — Datadog-shaped UI on :8080) |
| Install git hooks | `make hooks` |

**Mandatory gates before committing:**

```
go vet ./...                 # clean
golangci-lint run            # 0 issues
gofmt -l .                   # empty
go test -race -count=1 ./... # all pass
make coverage                # ≥ 45%
```

`make hooks` installs lefthook (pre-commit: lint/fmt/vet; pre-push: `go test -race -short`). If a check fails, fix it — never `--no-verify`.

---

## 4. Git / commit conventions

Strict format — full rule in [.claude/rules/git.md](.claude/rules/git.md).

- **Branch**: `[env]_[sprint].[tag]/[card]` — e.g. `dev_s9_26.feat/UP-70961` (from develop) · `stg_s48.fix/UP-14545` (from releasing_*) · `s48.feat/UP-14545` (from master).
- **Release branch**: `[prefix]_[version]` — e.g. `releasing_v1.0.0`.
- **Commit message**: `[tag]([module]): [card] [description]` — e.g. `feat(items): UP-70961 scaffold go-service-template` · `fix(ondemand): UP-14545 mongo transaction error`. Card goes IMMEDIATELY after `: `, not in `[brackets]` at the end.
- **Tags** (shared by branch + commit): `feat` / `fix` / `refactor` / `perf` / `docs` / `test` / `style` / `chore` / `build` / `ci` / `revert`.
- **Never auto-commit, never auto-push.** Only when the user explicitly says "commit" / "push". User reviews the staged diff first.
- **Never `--no-verify`.** Fix the failing hook.

---

## 5. Where to look when you're unsure

- **`.claude/rules/`** — the layered template lives here. [layout.md](.claude/rules/layout.md) §2 has the full feature-scaffolding walkthrough (per-file slots in §2.1, layer naming in §2.2, wiring in §2.3, comment density in §2.4). Topic-specific rules are in the other files.
- **`internal/app/app.go`** — the composition root. The dependency graph in one file IS the documentation.
- **`internal/platform/httpx/router.go`** — Mount table for the versioned API group.
- **`internal/infra/<x>/`** — external-system clients (mongox, valkey, kafka, grpcclient, otelx). Each does network/disk I/O against an actual external system.
- **`internal/platform/<x>/`** — service-shaped concerns (apperr, httpx, localization, logging, pagination, validate, config, buildinfo). References our types; couldn't be lifted out as a standalone module.
- **`internal/stdx/<x>/`** — pure-Go helpers (ptr, timex, retry, safego). Zero non-stdlib imports — could `go build` as a standalone module. See [layout.md](.claude/rules/layout.md) for the admission rules.

---

**If any rule above conflicts with a future user instruction, the user's instruction wins — but always note the deviation in a comment so future reviewers see what was overridden.**
